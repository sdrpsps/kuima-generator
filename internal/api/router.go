package api

import (
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"

	"wxcode/internal/config"
	"wxcode/internal/middleware"
)

// ============================================================
// 路由
// ============================================================

// newRouter 注册全部路由（页面、账号、管理端、三个 API），并套上访问日志。
func NewRouter() http.Handler {
	if config.APIOnly() {
		return newAPIOnlyRouter()
	}
	mux := http.NewServeMux()

	// 静态资源
	// 首页只匹配根路径（{$} 是"精确到此为止"），其余未注册路径落到 404 处理
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("/", handleNotFound)
	mux.HandleFunc("/favicon.ico", handleIconPNG)
	mux.HandleFunc("/icon.png", handleIconPNG)
	mux.HandleFunc("/apple-touch-icon.png", handleIconPNG)
	mux.HandleFunc("/vendor/bootstrap.min.css", handleVendorCSS)
	mux.HandleFunc("/badge.png", handleBadgePNG)
	mux.HandleFunc("/auth.css", handleAuthCSS)
	mux.HandleFunc("/layer.js", handleLayerJS)

	// 页面：要读当前身份（登录跳转、后台 404），所以套 withAuth
	mux.HandleFunc("GET /login", withAuth(handleLoginPage))
	mux.HandleFunc("GET /forgot", withAuth(handleForgotPage))
	mux.HandleFunc("GET /register", withAuth(handleRegisterPage))
	mux.HandleFunc("GET /me", withAuth(handleMePage))
	mux.HandleFunc("GET /admin", withAuth(handleAdminPage))

	mux.HandleFunc("/api/docs.md", handleAPIDocs)
	mux.HandleFunc("/api/openapi.json", handleOpenAPI)
	mux.HandleFunc("/api/postman.json", handlePostman)
	mux.HandleFunc("GET /api/captcha", handleCaptcha)
	// 水印实时预览（返回示例码 SVG）：登录用户可以看自己的配置效果，管理员还能在关闭水印时预览
	mux.HandleFunc("GET /api/watermark/preview", middleware.ProtectAPI(withAuth(handleWatermarkPreview)))
	// 预设 LOGO：列表与图片都公开（生成页要用，匿名也能生成），内容由管理员维护。
	// 不套 ProtectAPI：一个页面会同时拉若干缩略图，占限速额度会把正常用户限掉。
	mux.HandleFunc("GET /api/preset-logos", handlePresetLogos)
	mux.HandleFunc("GET /api/preset-logos/{id}", handlePresetLogoImage)
	// 生成结果的临时图片地址：手机端「点下载」「长按保存」都要求真实 http 图片地址，
	// blob: / data: 在微信内置浏览器和 iOS Safari 里都存不下来。
	// 不套 ProtectAPI：一次生成会伴随多次取图（预览、长按、重复打开），不该吃限速额度。
	mux.HandleFunc("GET /api/code/{name}", handleCodeAsset)
	mux.HandleFunc("/api/health", handleHealth)

	// 生成/解析都吃 CPU，套上限速 + 并发保护；withAuth 解析会话/Key
	mux.HandleFunc("/api/encode", middleware.ProtectAPI(middleware.WithHeavyGuard(withAuth(handleEncode))))
	mux.HandleFunc("/api/encode/batch", middleware.ProtectAPI(middleware.WithHeavyGuard(withAuth(handleEncodeBatch))))
	mux.HandleFunc("/api/decode", middleware.ProtectAPI(middleware.WithHeavyGuard(withAuth(handleDecode))))
	mux.HandleFunc("/api/qr", middleware.ProtectAPI(middleware.WithHeavyGuard(withAuth(handleQR))))

	// 账号
	mux.HandleFunc("POST /api/register", middleware.ProtectAPI(withAuth(handleRegister)))
	// 注册前的邮箱验证码（仅当后台开启「强制邮箱注册」且发信配置完整时可用）
	mux.HandleFunc("POST /api/register/email/send", middleware.ProtectAPI(withAuth(handleRegisterEmailSend)))
	mux.HandleFunc("POST /api/login", middleware.ProtectAPI(withAuth(handleLogin)))
	mux.HandleFunc("POST /api/logout", withAuth(handleLogout))
	mux.HandleFunc("GET /api/me", withAuth(handleMe))
	mux.HandleFunc("POST /api/me/password", middleware.ProtectAPI(withAuth(requireLogin(handleChangePassword))))
	mux.HandleFunc("POST /api/me/key", middleware.ProtectAPI(withAuth(requireLogin(handleRefreshKey))))
	mux.HandleFunc("POST /api/me/redeem", middleware.ProtectAPI(withAuth(requireLogin(handleRedeemCard))))
	// 找回密码：邮箱验证码（是否可用由后台开关 + 发信配置 + 用户绑定邮箱共同决定）
	mux.HandleFunc("POST /api/password/forgot", middleware.ProtectAPI(withAuth(handleForgotPasswordSend)))
	mux.HandleFunc("POST /api/password/reset", middleware.ProtectAPI(withAuth(handleForgotPasswordReset)))
	// 绑定邮箱（需登录）：发验证码 → 校验绑定 / 凭密码解绑
	mux.HandleFunc("POST /api/me/email/send", middleware.ProtectAPI(withAuth(requireLogin(handleMyEmailSend))))
	mux.HandleFunc("POST /api/me/email/bind", middleware.ProtectAPI(withAuth(requireLogin(handleMyEmailBind))))
	mux.HandleFunc("POST /api/me/email/unbind", middleware.ProtectAPI(withAuth(requireLogin(handleMyEmailUnbind))))
	mux.HandleFunc("GET /api/me/records", withAuth(requireLogin(handleMyRecords)))
	mux.HandleFunc("GET /api/me/records/export", withAuth(requireLogin(handleMyRecordsExport)))
	mux.HandleFunc("DELETE /api/me/records/{id}", middleware.ProtectAPI(withAuth(requireLogin(handleMyRecordDelete))))
	mux.HandleFunc("DELETE /api/me/records", middleware.ProtectAPI(withAuth(requireLogin(handleMyRecordsClear))))

	// 管理端：非管理员一律 404（连存在性都不暴露）
	admin := func(h http.HandlerFunc) http.HandlerFunc { return withAuth(requireAdmin(h)) }
	mux.HandleFunc("GET /api/admin/overview", admin(handleAdminOverview))
	// 在线更新：检查远端 VERSION / 下载产物替换自身并重启（UPDATE_DISABLE=1 可关闭）
	mux.HandleFunc("GET /api/admin/update/check", admin(handleUpdateCheck))
	mux.HandleFunc("GET /api/admin/update/status", admin(handleUpdateStatus))
	mux.HandleFunc("GET /api/admin/update/job", admin(handleUpdateJob))
	mux.HandleFunc("POST /api/admin/update/apply", middleware.ProtectAPI(admin(handleUpdateApply)))
	mux.HandleFunc("GET /api/admin/settings", admin(handleAdminGetSettings))
	mux.HandleFunc("PUT /api/admin/settings", admin(handleAdminSaveSettings))
	// 全站数据备份：导出 zip（VACUUM INTO 一致性快照）/ 导入 zip（校验后整体覆盖当前库）
	mux.HandleFunc("GET /api/admin/backup/export", admin(handleAdminBackupExport))
	mux.HandleFunc("POST /api/admin/backup/import", middleware.ProtectAPI(admin(handleAdminBackupImport)))
	// 发信测试：用当前表单值试发一封，不落库
	mux.HandleFunc("POST /api/admin/email/test", admin(handleAdminTestEmail))
	// 站点图标：上传后 /icon.png 立刻换成新图（URL 带版本号，不用清缓存）
	mux.HandleFunc("POST /api/admin/icon", admin(handleAdminUploadIcon))
	mux.HandleFunc("DELETE /api/admin/icon", admin(handleAdminDeleteIcon))
	// 预设 LOGO：管理员传一批，用户在生成页一键选用（上传走限速，避免被当图床刷）
	mux.HandleFunc("GET /api/admin/preset-logos", admin(handleAdminPresetLogos))
	mux.HandleFunc("POST /api/admin/preset-logos", middleware.ProtectAPI(admin(handleAdminPresetLogos)))
	mux.HandleFunc("PUT /api/admin/preset-logos/{id}", admin(handleAdminPresetLogoItem))
	mux.HandleFunc("DELETE /api/admin/preset-logos/{id}", admin(handleAdminPresetLogoItem))
	mux.HandleFunc("GET /api/admin/users", admin(handleAdminListUsers))
	mux.HandleFunc("POST /api/admin/users", admin(handleAdminCreateUser))
	mux.HandleFunc("PUT /api/admin/users/{id}", admin(handleAdminUpdateUser))
	mux.HandleFunc("POST /api/admin/users/{id}/password", admin(handleAdminResetPassword))
	mux.HandleFunc("GET /api/admin/records", admin(handleAdminRecords))
	mux.HandleFunc("POST /api/admin/records/purge", admin(handleAdminPurgeRecords))
	mux.HandleFunc("GET /api/admin/invites", admin(handleAdminInvites))
	mux.HandleFunc("POST /api/admin/invites", admin(handleAdminInvites))
	mux.HandleFunc("GET /api/admin/invites/export", admin(handleAdminInvitesExport))
	mux.HandleFunc("DELETE /api/admin/invites/{code}", admin(handleAdminInviteDelete))
	// 卡密：转码 / 解码次数，一张卡只能兑换一次
	mux.HandleFunc("GET /api/admin/cards", admin(handleAdminCards))
	mux.HandleFunc("POST /api/admin/cards", admin(handleAdminCards))
	mux.HandleFunc("GET /api/admin/cards/export", admin(handleAdminCardsExport))
	mux.HandleFunc("DELETE /api/admin/cards/{code}", admin(handleAdminCardDelete))
	// 黑名单：内容关键词（禁止生成）+ IP（拒绝访问）
	mux.HandleFunc("GET /api/admin/blacklist", admin(handleAdminBlacklist))
	mux.HandleFunc("POST /api/admin/blacklist", admin(handleAdminBlacklist))
	mux.HandleFunc("PUT /api/admin/blacklist/{id}", admin(handleAdminBlacklistUpdate))
	mux.HandleFunc("DELETE /api/admin/blacklist/{id}", admin(handleAdminBlacklistDelete))

	// 顺序：安全头 → 请求 ID → panic 兜底 → 访问日志 → gzip → 黑名单 IP 拦截
	//        → 跨站写保护 → 跨域 → 业务路由
	//
	// 请求 ID 要在最外层，才能同时出现在访问日志和业务日志里；
	// panic 兜底放在访问日志内侧，panic 时也能记一条 status=500 的访问日志。
	//
	// CORS 放在 sameOriginGuard 之后：preflight 请求不带 Cookie、
	// 也不带 API Key，sameOriginGuard 本来就会放行它，不会互相打架。
	return middleware.SecurityHeaders(middleware.WithRequestID(middleware.WithRecover(middleware.LogRequests(
		middleware.WithGzip(middleware.BlockBlacklistIP(middleware.SameOriginGuard(middleware.CorsMiddleware(mux))))))))
}

// newAPIOnlyRouter exposes the generation page and public generation and decoding API.
func newAPIOnlyRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("/favicon.ico", handleIconPNG)
	mux.HandleFunc("/icon.png", handleIconPNG)
	mux.HandleFunc("/apple-touch-icon.png", handleIconPNG)
	mux.HandleFunc("/vendor/bootstrap.min.css", handleVendorCSS)
	mux.HandleFunc("/badge.png", handleBadgePNG)
	mux.HandleFunc("/layer.js", handleLayerJS)
	mux.HandleFunc("/api/docs.md", handleAPIDocs)
	mux.HandleFunc("/api/openapi.json", handleOpenAPI)
	mux.HandleFunc("/api/postman.json", handlePostman)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("GET /api/code/{name}", handleCodeAsset)
	mux.HandleFunc("/api/encode", middleware.ProtectAPI(middleware.WithHeavyGuard(handleEncode)))
	mux.HandleFunc("/api/encode/batch", middleware.ProtectAPI(middleware.WithHeavyGuard(handleEncodeBatch)))
	mux.HandleFunc("/api/decode", middleware.ProtectAPI(middleware.WithHeavyGuard(handleDecode)))
	mux.HandleFunc("/api/qr", middleware.ProtectAPI(middleware.WithHeavyGuard(handleQR)))
	return middleware.SecurityHeaders(middleware.WithRequestID(middleware.WithRecover(middleware.LogRequests(
		middleware.WithGzip(middleware.CorsMiddleware(mux))))))
}
