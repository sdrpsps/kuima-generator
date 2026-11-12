package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"wxcode/internal/api"
	"wxcode/internal/config"
	"wxcode/internal/logger"
	"wxcode/internal/service"
	"wxcode/internal/store"
)

// ============================================================
// 入口：直接启动 HTTP 服务
// ============================================================

func main() {
	addr := config.ListenAddr()

	// 先开库：建表 + 初始化默认配置 + 首次创建管理员
	dbPath := config.DBPath()
	if config.APIOnly() {
		dbPath = ":memory:"
	}
	if err := store.Open(dbPath); err != nil {
		// 启动失败走 stderr：容器编排会把它当错误流收走
		logger.Error("数据库初始化失败", "error", err.Error(), "db_path", dbPath)
		os.Exit(1)
	}
	if !config.APIOnly() {
		store.CleanupSessions()
	}

	// 定期清理过期会话与过期记录（启动时先清一次，之后每 6 小时一次）
	janitorStop := make(chan struct{})
	if !config.APIOnly() {
		service.StartJanitor(janitorStop)

		// 每天自动检查一次新版本（只更新缓存，后台页面读缓存显示提示；UPDATE_DISABLE=1 可关）
		service.StartUpdateWatcher(janitorStop)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 请求头默认上限 1MB，这里收紧到 16KB：本服务的请求头只有几十字节
		MaxHeaderBytes: 16 << 10,
	}

	// 收到 SIGINT / SIGTERM（docker stop）时优雅退出，给在途请求留时间
	go watchSignals(srv, janitorStop)

	// 在线更新要用：重启前先释放监听端口（Windows 上必须先停监听，新进程才抢得到端口）
	service.SetShutdownFunc(func(ctx context.Context) error { return srv.Shutdown(ctx) })

	printBanner(addr)
	logger.Info("服务已启动",
		"version", config.Version,
		"build_time", config.BuildTime,
		"go_version", runtime.Version(),
		"pid", os.Getpid(),
		"addr", addr,
		"db_path", dbPath,
		"api_only", config.APIOnly(),
		"log_level", logger.CurrentLogConfig().Level().String(),
		"log_format", logger.CurrentLogConfig().Format(),
	)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("服务启动失败", "error", err.Error(), "addr", addr)
		os.Exit(1)
	}
	logger.Info("服务已退出")
}

// watchSignals 监听退出信号并优雅关闭服务。
//
// docker stop 发的是 SIGTERM（不是 SIGKILL），所以这里有窗口期让在途请求
// 正常结束 —— 直接退出会把用户的响应截断成一半。
func watchSignals(srv *http.Server, janitorStop chan struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	sig := <-ch
	shutdownServer(srv, janitorStop, sig)
}

// shutdownServer 执行优雅关闭：停后台任务 → 等在途请求结束 → 记日志。
func shutdownServer(srv *http.Server, janitorStop chan struct{}, sig os.Signal) {
	sigName := "unknown"
	if sig != nil {
		sigName = sig.String()
	}
	logger.Info("收到退出信号，正在优雅关闭", "signal", sigName, "timeout_s", int(config.ShutdownTimeout.Seconds()))
	close(janitorStop) // 停掉清理任务
	ctx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Warn("优雅关闭超时，仍有连接未结束", "error", err.Error())
		return
	}
	logger.Info("所有连接已正常关闭")
}

// printBanner 启动横幅，顺便把接口清单打出来，方便部署后自查。
//
// JSON 格式下不打印：横幅是多行 ASCII art，会把采集端的 JSON 行解析搞崩。
// 那种场景靠前面的 "服务已启动" 结构化日志就够了。
func printBanner(addr string) {
	if logger.CurrentLogConfig().Format() == "json" {
		return
	}
	if !config.EnvOnDefault("LOG_BANNER", true) {
		return
	}
	if config.APIOnly() {
		logger.Raw("葵码生成器：纯 API 模式\n监听地址: " + addr + "\n生成网页: /\n接口文档: /api/docs.md\n健康检查: /api/health\n")
		return
	}
	banner := "========================================\n" +
		"  WX_CODE 葵花码服务  v" + config.Version + "\n" +
		"----------------------------------------\n" +
		"  🌐 控制台    GET  /          ← 浏览器打开这个\n" +
		"  📦 生成接口  POST /api/encode\n" +
		"  📦 批量生成  POST /api/encode/batch\n" +
		"  🔍 解析接口  POST /api/decode\n" +
		"  🔍 好友码    POST /api/qr\n" +
		"  📄 接口文档  GET  /api/docs.md\n" +
		"  ❤️  健康检查  GET  /api/health\n" +
		"  👤 登录/注册  GET  /login  /register\n" +
		"  🛠  管理后台  GET  /admin   （仅管理员可见）\n" +
		"  🗄  数据库    " + config.DBPath() + "\n" +
		"----------------------------------------\n" +
		"  POST /api/encode  {text, format=png|jpg|gif|svg|json, size,\n" +
		"                     margin, fg, bg, logo, logoRatio, level, version, mask, badge}\n" +
		"  GET  /api/encode?text=...&format=png   可直接放进 <img src>\n" +
		"  POST /api/encode/batch  {format, size, items:[{text}, ...]}  最多 50 条\n" +
		"  POST /api/decode  图片二进制 / multipart file / JSON dataURL\n" +
		"  GET  /api/decode?url=...               给 URL，服务端下载后解析\n" +
		"  POST /api/qr      普通二维码（微信好友名片）→ text / url\n" +
		"----------------------------------------\n" +
		"  监听地址: " + addr + "   控制台: http://localhost" + addr + "/\n" +
		"========================================\n" // 末尾要带换行，否则后面的日志会粘在这一行后面
	logger.Raw(banner)
}
