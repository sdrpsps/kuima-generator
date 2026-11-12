package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wxcode/internal/config"
	"wxcode/internal/httpx"
	"wxcode/internal/imagex"
	"wxcode/internal/model"
	"wxcode/internal/netutil"
	"wxcode/internal/store"
)

// 接口文档（Markdown）：内容与代码里的真实参数、上限保持一致，直接提供给前端下载。

// apiMarkdown 生成完整的接口文档。baseURL 为空时用 localhost 占位。
func apiMarkdown(baseURL string, now time.Time) string {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	// 文档跟着服务实时状态走：开了「使用需要登录」，示例里就必须带 Key；
	// 没开就明确写"无需 Key"，免得用户以为不传会被拒。
	st := store.LoadSettings()

	var b strings.Builder
	w := func(format string, a ...interface{}) { fmt.Fprintf(&b, format, a...) }

	w("# 葵码生成器 · HTTP 接口文档\n\n")
	w("> 版本 **%s** · 生成时间 %s\n", config.Version, now.Format("2006-01-02 15:04:05"))
	w("> 基础地址 `%s`\n", baseURL)
	w("> OpenAPI 3：`%s/api/openapi.json` · Postman Collection：`%s/api/postman.json`\n\n", baseURL, baseURL)

	w("---\n\n## 一、接口一览\n\n")
	w("| 方法 | 路径 | 说明 |\n|---|---|---|\n")
	w("| POST | `/api/encode` | 输入内容，返回太阳码图片（png / jpg / svg / gif）或 JSON |\n")
	w("| GET | `/api/encode` | 同上，参数走 query，可直接放进 `<img src>` |\n")
	w("| POST | `/api/encode/batch` | **批量**生成，一次最多 %d 条，逐条返回结果 |\n", batchMaxItems)
	w("| POST | `/api/decode` | 上传太阳码图片，解析出内容 |\n")
	w("| POST | `/api/decode?family=auto` | 一次上传，自动尝试太阳码与普通二维码（`prefer=qr` 可优先 QR） |\n")
	w("| POST | `/api/qr` | 上传普通二维码（如微信「加好友」名片），取出其中的链接 |\n\n")
	w("> 公开接口仅包含生成与解析。账户、管理及其他站内接口不在本文档开放。\n\n")

	w("---\n\n## 二、通用约定\n\n")
	w("| 项 | 说明 |\n|---|---|\n")
	authRow := "生成 / 解析默认匿名可用；也可带 `X-API-Key` 调用，便于计费和限流"
	if config.APIOnly() {
		authRow = "纯 API 模式无需登录或 API Key，不设账号额度"
	} else if st.RequireLogin {
		authRow = "生成 / 解析**必须**带 `X-API-Key`（本站已开启「使用需要登录」）"
	}
	w("| 鉴权 | %s |\n", authRow)
	w("| 成功 | 图片接口返回二进制，JSON 接口返回对象 |\n")
	w("| 失败 | 统一 `{\"error\":\"原因\"}`，配合 4xx/5xx 状态码 |\n")
	w("| 状态码 | `400` 参数/图片不合法 · `403` 目标地址属内网被拒 · `404` 路径不存在 · `413` 请求体过大 · `429` 请求过于频繁 · `503` 服务繁忙 |\n")
	w("| 请求体 | JSON 基础上限 %dMB · multipart 基础上限 %dMB（按 logo 上限与徽标上传所需空间自动放宽，包含 base64 开销） |\n",
		imagex.MaxJSONBody>>20, imagex.MaxUploadBody>>20)
	w("| 请求 ID | 响应头 `X-Request-ID` 标识本次请求，服务端日志里用同一个 ID；报障时把它一起提供即可定位 |\n")
	w("| 上传大小 | 解析图片 **%dMB / %dMB**、logo **%dMB / %dMB**（匿名 / 登录，后台可配） |\n",
		st.AnonImageKB>>10, st.UserImageKB>>10, st.AnonLogoKB>>10, st.UserLogoKB>>10)
	w("| 图片尺寸 | 单边 ≤ %d px，总像素 ≤ %d 万；GIF 最多支持 %d 帧 |\n",
		imagex.MaxImageSide, netutil.MaxImagePixels/10000, netutil.MaxGIFFrames)
	w("| 内容长度 | `text` ≤ %d 字节（太阳码本身也只能装几十字节） |\n", netutil.MaxTextLen)
	w("| 限速 | 单 IP %d 次/分钟，超限返回 429 并带 `Retry-After` |\n", netutil.RateLimitPerMinute)
	w("| 并发 | 编码/解析同时最多 %d 个，超限返回 503 |\n", netutil.MaxConcurrentHeavy)
	w("| 跨域 | 默认**不允许**跨域；管理员配置来源白名单后才放行（见下） |\n\n")

	w("### 跨域（CORS）\n\n")
	if origins := store.LoadSettings().CORSOrigins; strings.TrimSpace(origins) != "" {
		w("本站已开启跨域，允许的来源：**`%s`**\n\n", origins)
	} else {
		w("本站**未开启**跨域：浏览器里的其它网站无法读取本接口的响应。\n")
		w("需要开启请让管理员在后台「站点设置 → 跨域来源白名单」里配置。\n\n")
	}
	w("开启后，命中的来源会拿到 `Access-Control-Allow-Origin` 等响应头，\n")
	w("`OPTIONS` 预检直接返回 204；未列出的来源请求照常返回，但没有跨域许可头（浏览器会拦掉）。\n\n")

	w("### 出站请求限制\n\n")
	w("`logo`、`badge`、`url` 里的 http(s) 地址由服务端下载，且**只能访问公网**：\n")
	w("回环 / 内网 / 链路本地 / 云元数据（`169.254.169.254`）/ CGNAT / 组播一律拒绝（返回 403），\n")
	w("最多跟随 3 次重定向，每一跳都重新校验。默认也**不允许把参数当本地文件路径**读取，\n")
	w("如需读取服务器本地图片，设置环境变量 `ALLOW_LOCAL_IMAGE=1`。\n\n")

	w("---\n\n## 三、POST /api/encode\n\n")
	w("生成太阳码。请求体可以是 JSON，也可以是 multipart（可带 `logo` 文件），或普通表单。\n\n")
	w("### 请求参数\n\n")
	w("| 参数 | 类型 | 必填 | 说明 |\n|---|---|---|---|\n")
	for _, f := range encodeBodyFields() {
		req := "否"
		if f.Required {
			req = "是"
		}
		w("| `%s` | %s | %s | %s |\n", f.Name, f.Type, req, f.Description)
	}
	w("\n")

	w("### 示例\n\n")
	w("```bash\n")
	w("curl -X POST %s/api/encode \\\n", baseURL)
	w("  -H 'Content-Type: application/json' \\\n")
	w("  -d '{\"text\":\"https://example.com/\",\"format\":\"png\",\"size\":600}' \\\n")
	w("  -o code.png\n\n")
	w("# 带中心 logo（本地文件上传）\n")
	w("curl -X POST %s/api/encode \\\n", baseURL)
	w("  -F 'text=https://example.com/' -F 'format=png' -F 'logo=@logo.png'\n")
	w("```\n\n")

	w("请求体示例（行尾 `//` 是字段说明，粘进代码时删掉即可）：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(encodeReqExample))

	w("### 返回\n\n")
	w("**`format=png|jpg|gif`** → 对应 `Content-Type` 的图片二进制：\n\n")
	w("| 响应头 | 含义 |\n|---|---|\n")
	w("| `X-WXCode-Version` | 实际使用的版本（0~2） |\n")
	w("| `X-WXCode-Level` | 实际纠错等级 `L`/`M`/`Q`/`H` |\n")
	w("| `X-WXCode-Mask` | 选中的掩码 0~31 |\n")
	w("| `X-WXCode-Typebits` | 18 位格式位 |\n")
	w("| `X-WXCode-Mode` | `NUMERIC` / `ALPHANUMERIC` / `BYTE` |\n")
	w("| `X-WXCode-Codewords` / `-DataCodewords` / `-ECCodewords` | 码字总数 / 数据 / 纠错 |\n")
	w("| `Content-Disposition` | `inline; filename=\"wxcode-v0Q-m14.png\"` |\n\n")
	w("**`format=svg`** → `image/svg+xml`，坐标为 float32 逐步舍入，与原生实现一致；\n")
	w("徽标默认内嵌 data URI，单独拷走也能显示。需要与原生逐字节一致时传 `badge=native`。\n\n")
	w("**`format=json`** →\n\n")
	w("| 字段 | 类型 | 说明 |\n|---|---|---|\n")
	for _, f := range encodeJSONFields {
		w("| `%s` | %s | %s |\n", f.Name, f.Type, f.Desc)
	}
	w("\n")
	w("响应示例（`format=json`，取值来自真实请求）：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(encodeJSONExample))

	w("### 错误示例\n\n")
	w("| 场景 | 状态码 | 响应体 |\n|---|---|---|\n")
	w("| 内容装不下 | 400 | `{\"error\":\"载荷超出一枚葵花码容量（v0..v2 均装不下，请缩短内容）\"}` |\n")
	w("| 超过接口上限 | 400 | `{\"error\":\"内容过长（上限 %d 字节，葵花码本身也只能装几十字节）\"}` |\n", netutil.MaxTextLen)
	w("| text 为空 | 400 | `{\"error\":\"text 不能为空\"}` |\n")
	w("| 命中黑名单关键词 | 403 | `{\"error\":\"内容包含禁止发布的词语\"}` |\n")
	w("| 超过限速 | 429 | `{\"error\":\"请求过于频繁，请稍后再试\"}`，带 `Retry-After` |\n\n")

	w("---\n\n## 四、POST /api/encode/batch（批量生成）\n\n")
	w("一次请求生成多个太阳码，适合「给一批链接各生成一个」的场景。单次最多 **%d** 条，请求体上限 %dMB。\n\n", batchMaxItems, batchMaxBody>>20)
	w("```bash\ncurl -X POST %s/api/encode/batch \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"format\":\"json\",\"size\":300,\"items\":[\n", baseURL)
	w("        {\"text\":\"https://a.example.com/1\"},\n")
	w("        {\"text\":\"https://b.example.com/2\"}\n")
	w("      ]}'\n```\n\n")
	w("请求体示例（行尾 `//` 是字段说明）：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(batchReqExample))
	w("**顶层是公共参数**（`format` / `size` / `margin` / `fg` / `bg` / `logo` / `logoRatio` / `level` / `version` / `mask` / `badge` / `fillBadge`），\n")
	w("`items` 里每条只给 `text`；单条也可以用 `format` / `size` / `margin` / `fg` / `bg` / `logo` / `logoRatio` / `badge` / `fillBadge` 逐条覆盖顶层值。\n\n")
	w("**逐条独立**：第 3 条失败不影响前 2 条 —— 每条都有自己的 `ok` / `error`。\n")
	w("**逐条计费**：额度按**实际成功且非示例/重复内容**的条数扣（响应里的 `charged` 给出数字），失败的条目不扣。\n")
	w("响应统一是 JSON（`format` 只决定图片怎么出），`format=json` 时同时给 `svg` 与 `pngBase64`。\n\n")
	w("响应字段：\n\n")
	w("| 字段 | 类型 | 说明 |\n|---|---|---|\n")
	for _, f := range batchFields {
		w("| `%s` | %s | %s |\n", f.Name, f.Type, f.Desc)
	}
	w("\n")
	w("响应示例：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(batchRespExample))

	w("---\n\n## 五、GET /api/encode\n\n")
	w("参数与 POST 完全一致，只是放在 query 里，方便直接嵌入页面：\n\n")
	w("```html\n<img src=\"%s/api/encode?text=https%%3A%%2F%%2Fexample.com%%2F&format=png&size=400\">\n```\n\n", baseURL)
	w("响应体与响应头同 POST。`text` 记得做 URL 编码。\n\n")

	w("---\n\n## 六、POST /api/decode\n\n")
	w("解析太阳码。支持三种投喂方式，任选其一：\n\n")
	w("| 参数 | 位置 | 说明 |\n|---|---|---|\n")
	w("| `file` | multipart | 表单文件字段（也接受 `image`）：`-F 'file=@code.png'` |\n")
	w("| （原始二进制） | body | `Content-Type: image/png`，body 就是图片字节 |\n")
	w("| `image` | JSON | `{\"image\":\"data:image/png;base64,...\"}` 或 http(s) 链接 |\n")
	w("| `url` | query/JSON | 给图片地址，由服务端下载后解析（**仅公网地址**） |\n\n")
	w("### 示例\n\n```bash\n")
	w("curl -X POST %s/api/decode -F 'file=@code.png'\n", baseURL)
	w("curl -X POST %s/api/decode --data-binary @code.png -H 'Content-Type: image/png'\n", baseURL)
	w("curl '%s/api/decode?url=https%%3A%%2F%%2Fexample.com%%2Fcode.png'\n", baseURL)
	w("```\n\n")
	w("JSON 投喂方式的请求示例（行尾 `//` 是说明，不是 JSON 内容）：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(decodeReqExample))
	w("### 返回\n\n")
	w("| 字段 | 类型 | 说明 |\n|---|---|---|\n")
	for _, f := range decodeFields {
		w("| `%s` | %s | %s |\n", f.Name, f.Type, f.Desc)
	}
	w("\n")
	w("响应示例（真实请求 `https://example.com/` 的 600×600 图）：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(decodeRespExample))
	w("### 自动识别（推荐给上传入口）\n\n")
	w("加 `family=auto` 时只上传一次图片，服务端依次尝试太阳码与普通二维码：\n\n")
	w("```bash\ncurl -X POST '%s/api/decode?family=auto' -F 'file=@unknown.png'\n```\n\n", baseURL)
	w("好友码入口可加 `prefer=qr` 让普通二维码先试。响应字段是两种结果的并集：\n")
	w("`family` 为 `WX_CODE` 或 `QR_CODE`，`payload` 统一给出可复制内容；\n")
	w("二维码额外有 `text` / `url` / `modules`，太阳码额外有 `typebits` / `count` / `qrText` / `box`。\n\n")

	w("---\n\n## 七、POST /api/qr\n\n")
	w("解析普通二维码（微信「加好友」名片即 `https://u.wechat.com/...`）。请求方式与 `/api/decode` 相同。\n\n")
	w("```bash\ncurl -X POST %s/api/qr -F 'file=@friend.png'\n```\n\n", baseURL)
	w("### 返回\n\n")
	w("| 字段 | 类型 | 说明 |\n|---|---|---|\n")
	for _, f := range qrFields {
		w("| `%s` | %s | %s |\n", f.Name, f.Type, f.Desc)
	}
	w("\n")
	w("响应示例：\n\n")
	w("```jsonc\n%s\n```\n\n", jsoncAlign(qrRespExample))

	w("---\n\n## 八、Postman 导入\n\n")
	w("接口页右上角点「Postman」下载，或在 Postman 里选择 **Import → File** 导入 `wxcode.postman_collection.json`。\n\n")
	w("Postman Collection 只包含公开的生成 / 解析接口。导入后修改两个变量即可：\n\n")
	w("| 变量 | 说明 |\n|---|---|\n")
	w("| `baseUrl` | 站点地址，例如 `%s` |\n", baseURL)
	w("| `apiKey` | 可选；需要按 Key 计费或本站要求登录时填写「我的」页面复制的 Key |\n\n")

	w("---\n\n## 九、参数速查\n\n")
	w("| 参数 | 取值 | 说明 |\n|---|---|---|\n")
	w("| `format` | `png` `jpg` `svg` `gif` `json` | 输出格式；`gif` + 动图 logo / 徽标 = 循环动图，最多合成 20 帧（超出按完整周期抽帧） |\n")
	w("| `level` | `L` `M` `Q` `H` | 最低纠错等级，容量够时自动升级 |\n")
	w("| `version` | `auto` `0` `1` `2` | 码格版本；`auto` 取最小可容纳 |\n")
	w("| `mask` | `-1` 或 `0`~`31` | `-1` 自动按评分选最优 |\n")
	w("| `badge` | `default` `native` `none` 或图片地址 / data URL / base64 | 角落徽标；支持 GIF，format=gif 时保留动画 |\n")
	w("| `fillBadge` | `true` / `false`（默认） | 隐藏徽标时用同风格码点填补右下角空位；显示徽标时忽略 |\n")
	w("| `fg` / `bg` | `#rrggbb` | 前景 / 背景色 |\n\n")

	w("### 容量参考\n\n")
	w("| 内容类型 | 大约上限 |\n|---|---|\n")
	w("| 纯数字 | 约 80 字符 |\n")
	w("| URL / 大写字母数字 | 约 58 字符 |\n")
	w("| 中文 / 小写字母（字节模式） | 约 44 字节 |\n\n")

	w("---\n\n## 十、前端调用示例\n\n")
	w("```js\n")
	w("// 生成：直接拿 blob 预览 / 下载\n")
	w("const resp = await fetch('/api/encode', {\n")
	w("  method: 'POST',\n")
	w("  headers: {'Content-Type': 'application/json'},\n")
	w("  body: JSON.stringify({text: 'https://example.com/', format: 'png', size: 600})\n")
	w("});\n")
	w("const blob = await resp.blob();\n")
	w("img.src = URL.createObjectURL(blob);\n")
	w("console.log('版本', resp.headers.get('X-WXCode-Version'));\n\n")
	w("// 解析：上传文件\n")
	w("const fd = new FormData();\n")
	w("fd.append('file', fileInput.files[0]);\n")
	w("const data = await (await fetch('/api/decode', {method: 'POST', body: fd})).json();\n")
	w("console.log(data.payload, data.corrections);\n")
	w("```\n")
	w("---\n\n## 十一、鉴权与额度（生成 / 解析）\n\n")
	w("| 身份 | 怎么传 | 是否扣额度 |\n|---|---|---|\n")
	w("| API Key | `X-API-Key: wx_...`，或 `Authorization: Bearer wx_...`，或 `?key=` | **是**，每次成功调用扣 1 |\n")
	w("| 会话 Cookie | 网页登录后自动携带（`wx_sid`，HttpOnly） | 「使用需要登录」开启时扣；关闭时网页调用不扣 |\n\n")
	w("优先级：Key > Cookie。带了无效 Key 会直接 401，不会降级成匿名。\n\n")
	w("计费例外：同一账号对**同一内容**重复生成 / 解析不再扣额度，也不会重复写记录；\n")
	w("`https://example.com/` 这类示例内容与本站地址（首页默认预览）同样不计费、不记录。\n\n")
	w("额度用尽：`403 {\"error\":\"额度已用完，请联系管理员增加额度\"}`；\n")
	w("需要登录：`401 {\"error\":\"该功能需要登录后使用\",\"needLogin\":true}`。\n\n")

	doc := b.String()
	if config.APIOnly() {
		doc = strings.ReplaceAll(doc, "账户、管理及其他站内接口不在本文档开放。", "纯 API 模式不提供账户和管理接口。")
		doc = strings.ReplaceAll(doc, "默认**不允许**跨域；管理员配置来源白名单后才放行（见下）", "默认**不允许**跨域；通过 `API_CORS_ORIGINS` 配置来源白名单（见下）")
		doc = strings.ReplaceAll(doc, "（匿名 / 登录，后台可配）", "（纯 API 模式使用匿名限额）")
		doc = strings.ReplaceAll(doc, "可选；需要按 Key 计费或本站要求登录时填写「我的」页面复制的 Key", "纯 API 模式无需填写")
		doc = strings.Replace(doc, "需要开启请让管理员在后台「站点设置 → 跨域来源白名单」里配置。", "需要开启请设置环境变量 `API_CORS_ORIGINS`。", 1)
		if i := strings.Index(doc, "## 十一、鉴权与额度（生成 / 解析）"); i >= 0 {
			doc = doc[:i] + "## 十一、鉴权与额度\n\n纯 API 模式无需登录或 API Key，不设账号额度；如需鉴权，请配置反向代理或 API 网关。\n"
		}
		return doc
	}
	return adjustDocsAuth(doc, st)
}

// adjustDocsAuth 按「使用需要登录」开关改写文档：
//   - 开启：顶部加醒目提示，所有 curl 示例补上 X-API-Key（GET 用 ?key=）；
//   - 关闭：明确写"无需 Key"，示例保持原样不动。
func adjustDocsAuth(doc string, st model.Settings) string {
	keyArg := "-H 'X-API-Key: wx_你的Key'"
	if !st.RequireLogin {
		return strings.Replace(doc, "> 基础地址 `",
			"> **本站未开启登录限制：生成 / 解析可匿名调用，无需 API Key。**\n>\n> 基础地址 `", 1)
	}

	doc = strings.Replace(doc, "> 基础地址 `",
		"> **本站已开启「使用需要登录」：生成 / 解码 / 好友码都需要登录；调用接口必须带 "+
			"`X-API-Key`（登录后在「我的」页面复制），每次成功调用扣 1 次额度。**\n>\n> 基础地址 `", 1)

	// POST 示例：JSON 的 curl 是多行，插在 Content-Type 之后
	doc = strings.ReplaceAll(doc,
		"-H 'Content-Type: application/json' \\\n",
		"-H 'Content-Type: application/json' \\\n  "+keyArg+" \\\n")
	// multipart 的 encode 示例没有 Content-Type 行，单独插
	doc = strings.ReplaceAll(doc,
		"/api/encode \\\n  -F 'text=",
		"/api/encode \\\n  "+keyArg+" \\\n  -F 'text=")
	// 解码三种投喂方式
	doc = strings.ReplaceAll(doc, "/api/decode -F 'file=@code.png'",
		"/api/decode "+keyArg+" -F 'file=@code.png'")
	doc = strings.ReplaceAll(doc, "/api/decode --data-binary",
		"/api/decode "+keyArg+" --data-binary")
	// GET 类走 query
	doc = strings.ReplaceAll(doc, "/api/decode?url=", "/api/decode?key=wx_你的Key&url=")
	doc = strings.ReplaceAll(doc, "/api/encode?text=", "/api/encode?key=wx_你的Key&text=")
	// 好友码
	doc = strings.ReplaceAll(doc, "/api/qr -F 'file=",
		"/api/qr "+keyArg+" -F 'file=")
	// 前端 JS 示例
	doc = strings.ReplaceAll(doc,
		"headers: {'Content-Type': 'application/json'},",
		"headers: {'Content-Type': 'application/json', 'X-API-Key': 'wx_你的Key'},")
	doc = strings.ReplaceAll(doc,
		"fetch('/api/decode', {method: 'POST', body: fd})",
		"fetch('/api/decode', {method: 'POST', headers: {'X-API-Key': 'wx_你的Key'}, body: fd})")
	return doc
}

// handleAPIDocs 输出 Markdown 接口文档。
//
// GET /api/docs.md          作为附件下载
// GET /api/docs.md?inline=1 在浏览器里直接显示
func handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.WriteErr(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
		return
	}
	// 基础地址按当前访问域名还原（反代下取 X-Forwarded-*），与首页示例保持一致
	doc := apiMarkdown(httpx.RequestBaseURL(r), time.Now())

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("inline") == "" {
		w.Header().Set("Content-Disposition", `attachment; filename="wxcode-api.md"`)
	} else {
		w.Header().Set("Content-Disposition", `inline; filename="wxcode-api.md"`)
	}
	io.WriteString(w, doc)
}
