package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"html"
	"net/http"
	"strings"
	"sync"

	"wxcode/internal/config"
	"wxcode/internal/httpx"
	"wxcode/internal/store"
	webassets "wxcode/web"
)

// handleIndex 生成/解析控制台
// GET /
func handleIndex(w http.ResponseWriter, r *http.Request) {
	// 只认根路径，其它未注册路径仍返回 404，避免以后加子路由被这里吃掉
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// 页面里的示例地址跟着当前访问域名走，不再写死 localhost:8080
	w.Write(renderIndex(r))
}

// baseURLToken 是 index.html 里预留的占位符，服务端按当前请求的域名/协议替换。
//
// 用它而不是在 JS 里拼地址：关掉 JS 也能看到正确链接，复制按钮拿到的也是替换后的文本，
// 页面不用为"示例地址"背任何运行时逻辑。
const baseURLToken = "{{BASE_URL}}"

// bootstrapVersionToken 给本地 Bootstrap 样式表做内容哈希 URL。
const bootstrapVersionToken = "{{BOOTSTRAP_V}}"

// renderIndex 把内嵌页面的占位符换成当前站点根地址。
//
// requestBaseURL 已做字符白名单校验，返回值里不可能出现 < > & " ' ，
// 所以直接替换进 HTML 是安全的，不需要再转义。
func renderIndex(r *http.Request) []byte {
	st := store.LoadSettings()
	body := strings.ReplaceAll(string(webassets.IndexHTML), baseURLToken, httpx.RequestBaseURL(r))
	body = strings.ReplaceAll(body, bootstrapVersionToken, bootstrapVersion())
	body = strings.ReplaceAll(body, siteNameToken, html.EscapeString(st.SiteName))
	body = strings.ReplaceAll(body, iconVerToken, st.IconVer)
	if config.APIOnly() {
		body = strings.Replace(body, "<body>", `<body data-api-only="true">`, 1)
		body = strings.Replace(body, "</style>", `[data-api-only="true"] #navAccount, [data-api-only="true"] #navAccountIcon, [data-api-only="true"] .api-authbar { display: none !important; }</style>`, 1)
		body = strings.ReplaceAll(body, "左侧选择接口，右侧查看参数并直接发送请求。留空 Key 时使用当前浏览器登录会话。", "左侧选择接口，右侧查看参数并直接发送请求。")
		body = strings.ReplaceAll(body, "Try it 请求与页面同源，会自动携带登录 Cookie", "无需登录即可调用接口")
		body = strings.ReplaceAll(body, "默认匿名可用；管理员可开启「使用需要登录」。程序调用建议在请求头带 <code>X-API-Key</code>，需登录时网页会携带会话 Cookie。", "无需登录或 API Key。")
		body = strings.ReplaceAll(body, "中心 logo 与待解析图片按后台配置限制", "中心 logo 限 8MB，待解析图片限 24MB")
	}
	body = decoratePage(body, st)
	body = injectFooter(body, st)
	return []byte(body)
}

var (
	iconMu   sync.RWMutex
	iconCurV string
	iconCurD []byte
)

var (
	bootstrapVerOnce sync.Once
	bootstrapVer     string
)

// bootstrapVersion 返回嵌入式 Bootstrap 的内容哈希，用作长缓存 URL 的版本号。
func bootstrapVersion() string {
	bootstrapVerOnce.Do(func() {
		sum := sha256.Sum256(webassets.BootstrapCSS)
		bootstrapVer = hex.EncodeToString(sum[:4])
	})
	return bootstrapVer
}

// siteIcon 返回当前图标字节与版本号：上传过就用自定义的，否则回落到内嵌默认图。
//
// 图标会被每个页面、每次刷新请求，绝不能每次都 base64 解码一遍；这里只在
// settings 里的版本号变化时才重新解码并缓存。版本号为空表示"用的是默认图"。
func siteIcon() ([]byte, string) {
	st := store.LoadSettings()

	iconMu.RLock()
	if iconCurD != nil && iconCurV == st.IconVer {
		d, v := iconCurD, iconCurV
		iconMu.RUnlock()
		return d, v
	}
	iconMu.RUnlock()

	iconMu.Lock()
	defer iconMu.Unlock()
	if iconCurD != nil && iconCurV == st.IconVer {
		return iconCurD, iconCurV // 并发下别人已经填好了
	}
	data, ver := webassets.IconPNG, ""
	if st.HasIcon {
		// 直接从配置快照里取（loadSettings 已经把 site_icon 一起读出来了），
		// 不再为每一张 /icon.png 单独发一条 SELECT。
		if raw, err := base64.StdEncoding.DecodeString(st.IconData); err == nil && len(raw) > 0 {
			data, ver = raw, st.IconVer
		}
	}
	iconCurV, iconCurD = ver, data
	return iconCurD, iconCurV
}

// handleIconPNG 站点图标（导航栏 logo、浏览器页签、iOS 主屏图标共用一张图）。
//
// 优先用管理后台上传的自定义图标（settings 里的 site_icon），没上传才回落到内嵌的
// web/icon.png。页面里引用的是 /icon.png?v=<iconVer>：换了图标 URL 就变，
// 浏览器不会拿着旧图标不放；带对版本号才给长缓存。
//
// /favicon.ico 也走这里 —— 浏览器即使不看 <link rel="icon"> 也会硬请求这个路径，
// 给同一份 PNG 就不会再有 404 噪音（Content-Type 已声明 image/png，浏览器会照用）。
//
// GET /icon.png、/favicon.ico、/apple-touch-icon.png
func handleIconPNG(w http.ResponseWriter, r *http.Request) {
	body, ver := siteIcon()
	w.Header().Set("Content-Type", "image/png")
	if ver != "" && r.URL.Query().Get("v") == ver {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// 没带版本号（或自定义图标刚换过）一律回源校验，避免长期用旧图
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Write(body)
}

// handleVendorCSS 本地托管的 Bootstrap 4.6.2 样式表。
//
// 刻意不引 CDN：这套服务经常部署在内网/无外网的机器上，依赖 CDN 会让"断网即裸奔"成为必然，
// 公网请求也拖慢首屏。打包进二进制后，任何网络环境下页面都长一样。
//
// GET /vendor/bootstrap.min.css
func handleVendorCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	// URL 带内容哈希时给一年 immutable，否则保留短缓存兼容旧链接。
	if r.URL.Query().Get("v") == bootstrapVersion() {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Write(webassets.BootstrapCSS)
}

// handleBadgePNG 输出内嵌的角落徽标，方便离线场景用相对地址引用。
//
// GET /badge.png
func handleBadgePNG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(webassets.BadgePNG)
}
