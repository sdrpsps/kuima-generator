package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"wxcode/internal/config"
	"wxcode/internal/httpx"
	"wxcode/internal/netutil"
	"wxcode/internal/qrcode"
	"wxcode/internal/store"
)

type apiParamDoc struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Example     any      `json:"example,omitempty"`
}

type apiBodyDoc struct {
	Name        string   `json:"name"`
	In          string   `json:"in,omitempty"` // json | multipart
	Type        string   `json:"type"`
	Format      string   `json:"format,omitempty"`
	Description string   `json:"description"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Example     any      `json:"example,omitempty"`
}

type apiTryField struct {
	Name        string   `json:"name"`
	In          string   `json:"in"` // body | query | path | file
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Value       any      `json:"value,omitempty"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Rows        int      `json:"rows,omitempty"`
}

type apiTryDoc struct {
	Mode     string        `json:"mode"` // none | json | query | multipart | json-editor
	Fields   []apiTryField `json:"fields,omitempty"`
	Template string        `json:"template,omitempty"`
}

// apiFieldDoc 是响应里的单个字段说明。
// 既用于接口页的「响应字段」表格，也用于生成 OpenAPI 的 schema.properties。
// Name 支持 results[].index 这种写法，数组元素字段会自动生成嵌套 schema。
type apiFieldDoc struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"description"`
}

type apiResponseDoc struct {
	Code        string   `json:"code"`
	Description string   `json:"description"`
	Types       []string `json:"types,omitempty"`
	// Example 是 JSONC（标准 JSON + 行尾 // 中文注释）。
	// 展示时保留注释；导出 OpenAPI / Postman 时由 jsoncStrip 去掉注释。
	Example string        `json:"example,omitempty"`
	Fields  []apiFieldDoc `json:"fields,omitempty"`
}

type apiOperationDoc struct {
	ID          string
	Method      string
	Path        string
	Tag         string
	Summary     string
	Description string
	Auth        string // none | optional | user | admin
	Content     string // none | json | multipart | json+multipart+binary
	Params      []apiParamDoc
	Body        []apiBodyDoc
	Responses   []apiResponseDoc
	Try         *apiTryDoc
	// RequestExample 是请求示例：JSON 请求体写 JSONC（带 // 中文注释），
	// query 类写带 {{BASE_URL}} 前缀的完整 URL。接口页、OpenAPI、Postman 共用这一份。
	RequestExample string
}

// ---------- 小工具 ----------

func apiParam(name, in, typ, desc string, example any, enum ...string) apiParamDoc {
	return apiParamDoc{Name: name, In: in, Type: typ, Description: desc, Enum: enum, Example: example}
}

func apiBody(name, in, typ, desc string, required bool, example any, enum ...string) apiBodyDoc {
	return apiBodyDoc{Name: name, In: in, Type: typ, Description: desc, Required: required, Example: example, Enum: enum}
}

func apiResp(code, desc string, types []string, example string) apiResponseDoc {
	return apiResponseDoc{Code: code, Description: desc, Types: types, Example: example}
}

// apiRespF 带上响应字段说明（只给公开接口用，保证接口页能逐字段解释）。
func apiRespF(code, desc string, types []string, example string, fields ...apiFieldDoc) apiResponseDoc {
	return apiResponseDoc{Code: code, Description: desc, Types: types, Example: example, Fields: fields}
}

// apiField 是响应字段说明的简写。
func apiField(name, typ, desc string) apiFieldDoc {
	return apiFieldDoc{Name: name, Type: typ, Desc: desc}
}

// ---------- JSON 示例（JSONC）工具 ----------
//
// 全站示例只维护一份：写成「标准 JSON + 行尾 // 中文注释」。
//   - 接口页 / Markdown 文档：保留注释，读者直接看到每个字段的含义；
//   - OpenAPI example / Postman raw body：jsoncStrip 去掉注释、尾逗号，得到合法 JSON。
// 这样就不会出现「页面一套示例、Postman 又一套，改了一处忘了另一处」。

// findJSONCComment 返回一行中字符串之外的 // 起始下标（-1 表示没有注释）。
// 必须跳字符串，否则 "https://example.com/" 里的 // 会被误判成注释。
func findJSONCComment(line string) int {
	inStr, esc := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			return i
		}
	}
	return -1
}

// jsoncAlign 把所有行尾注释对齐到同一列，读起来是整齐的一竖排。
// 注释列取「行宽 90 分位」再夹到 24~46 之间：绝大多数行能对齐，
// 偶尔特别长的一行（base64、SVG 文本）就紧跟在值后面，不会把整块注释推得太远。
// 纯注释行（缩进后直接是 //）保持原样，不参与对齐。
func jsoncAlign(src string) string {
	type row struct {
		body    string
		comment string
		has     bool
	}
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	rows := make([]row, len(lines))
	lens := make([]int, 0, len(lines))
	for i, line := range lines {
		idx := findJSONCComment(line)
		if idx < 0 {
			rows[i] = row{body: strings.TrimRight(line, " \t")}
			continue
		}
		body := strings.TrimRight(line[:idx], " \t")
		if strings.TrimSpace(body) == "" {
			rows[i] = row{body: strings.TrimRight(line, " \t")}
			continue
		}
		rows[i] = row{body: body, comment: strings.TrimSpace(line[idx+2:]), has: true}
		lens = append(lens, len(body))
	}
	width := 24
	if len(lens) > 0 {
		sort.Ints(lens)
		p90 := lens[(len(lens)*9)/10]
		if p90 > width {
			width = p90
		}
		// 最长行只比 p90 长一点点时，干脆对齐到最长行，避免「就差一个字段对不齐」
		if longest := lens[len(lens)-1]; longest-width <= 10 && longest < 46 {
			width = longest
		}
	}
	if width > 46 {
		width = 46
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		if !r.has {
			b.WriteString(r.body)
			continue
		}
		pad := width - len(r.body) + 2
		if pad < 2 {
			pad = 2
		}
		b.WriteString(r.body)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString("// ")
		b.WriteString(r.comment)
	}
	return b.String()
}

// jsoncStrip 去掉注释与尾逗号，返回紧凑且合法的 JSON 文本。
func jsoncStrip(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if idx := findJSONCComment(line); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	s := strings.Join(out, "\n")
	// 注释掉的行整行被删掉后，可能留下 ",}" / ",]" 之类，统一收掉尾逗号
	s = trailingComma.ReplaceAllString(s, "$1")
	var compact, indented bytes.Buffer
	if err := json.Compact(&compact, []byte(s)); err != nil {
		return s
	}
	if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
		return compact.String()
	}
	return indented.String()
}

var trailingComma = regexp.MustCompile(`,\s*([}\]])`)

// jsoncValue 解析 JSONC 为通用值；解析失败返回 nil,false（调用方据此降级）。
func jsoncValue(src string) (any, bool) {
	if strings.TrimSpace(src) == "" {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(jsoncStrip(src)), &v); err != nil {
		return nil, false
	}
	return v, true
}

// splitFieldPath 把 results[].index 拆成 [{results true} {index false}]。
func splitFieldPath(name string) []struct {
	Name  string
	Array bool
} {
	parts := strings.Split(name, ".")
	toks := make([]struct {
		Name  string
		Array bool
	}, 0, len(parts))
	for _, p := range parts {
		tok := struct {
			Name  string
			Array bool
		}{Name: p}
		if strings.HasSuffix(p, "[]") {
			tok.Name = strings.TrimSuffix(p, "[]")
			tok.Array = true
		}
		toks = append(toks, tok)
	}
	return toks
}

// schemaFromFields 把扁平字段说明还原成嵌套的 JSON Schema。
// 支持单层数组（results[].index → array.items.object.properties.index）。
func schemaFromFields(fields []apiFieldDoc) map[string]any {
	root := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, f := range fields {
		toks := splitFieldPath(f.Name)
		node := root
		for i, tok := range toks {
			props, ok := node["properties"].(map[string]any)
			if !ok {
				props = map[string]any{}
				node["properties"] = props
			}
			last := i == len(toks)-1
			if tok.Array {
				arr, ok := props[tok.Name].(map[string]any)
				if !ok {
					arr = map[string]any{"type": "array"}
					props[tok.Name] = arr
				}
				if last {
					arr["items"] = map[string]any{"type": f.Type, "description": f.Desc}
					break
				}
				items, ok := arr["items"].(map[string]any)
				if !ok {
					items = map[string]any{"type": "object", "properties": map[string]any{}}
					arr["items"] = items
				}
				node = items
				continue
			}
			if last {
				prop := map[string]any{"type": f.Type}
				if f.Desc != "" {
					prop["description"] = f.Desc
				}
				props[tok.Name] = prop
				break
			}
			child, ok := props[tok.Name].(map[string]any)
			if !ok {
				child = map[string]any{"type": "object", "properties": map[string]any{}}
				props[tok.Name] = child
			}
			if _, ok := child["properties"]; !ok {
				child["properties"] = map[string]any{}
			}
			node = child
		}
	}
	return root
}

func tryF(name, in, label, typ string, value any, opts ...string) apiTryField {
	return apiTryField{Name: name, In: in, Label: label, Type: typ, Value: value, Options: opts}
}

func apiJSONTry(fields ...apiTryField) *apiTryDoc {
	return &apiTryDoc{Mode: "json", Fields: fields}
}

func apiQueryTry(fields ...apiTryField) *apiTryDoc {
	return &apiTryDoc{Mode: "query", Fields: fields}
}

func apiMultipartTry(fileLabel string, fields ...apiTryField) *apiTryDoc {
	all := append([]apiTryField{{
		Name: "file", In: "file", Label: fileLabel, Type: "file", Required: true,
	}}, fields...)
	return &apiTryDoc{Mode: "multipart", Fields: all}
}

// ---------- 示例（JSONC，唯一来源） ----------
//
// 下面的取值都跑过真实请求（/api/encode?format=json、/api/decode、/api/encode/batch），
// 不是手写的「看起来像」的假数据：version=0 / level=Q / mask=14 / typebits=122236
// 就是 https://example.com/ 在当前实现下的真实编码结果。

// encodeReqExample POST /api/encode 的请求体示例。
const encodeReqExample = `{
  "text": "https://example.com/",   // 必填：要编码的内容（接口上限 8192 字节）
  "format": "png",                  // 输出格式：png / jpg / svg / gif / json
  "size": 600,                      // 位图边长 120~2000（svg 忽略）
  "margin": 0,                      // 位图四周白边 0~200
  "fg": "#000000",                  // 前景色（码点）#rrggbb
  "bg": "#ffffff",                  // 背景色 #rrggbb
  "level": "L",                     // 允许的最低纠错等级 L/M/Q/H，容量够会自动升级
  "version": "auto",                // auto 取最小可容纳；也可钉死 0 / 1 / 2
  "mask": -1,                       // -1 自动选最优掩码；0~31 固定
  "logo": "",                       // 中心 logo：data URL / http(s) / 裸 base64，空=不画
  "logoRatio": 1,                   // logo 半径缩放 0.2~3，1 表示 108/600
  "badge": "default",               // 角落徽标：default / native / none / 图片地址
  "fillBadge": false,               // 隐藏徽标时填补右下角空位，配合 badge=none 使用
  "style": "classic",               // 笔画风格：classic / hand / dot / block / line
  "color": "solid",                 // 配色：solid（用 fg）/ rainbow（色环）/ random（彩虹）/ ghost（隐形）
  "ghost": 6                        // 隐形深度：墨色比底色深几级（color=ghost 时生效）
}`

// encodeGetExample GET /api/encode 的请求示例（参数全在 query 上）。
const encodeGetExample = `{{BASE_URL}}/api/encode?text=https%3A%2F%2Fexample.com%2F&format=png&size=400`

// encodeJSONExample POST /api/encode?format=json 的响应（runtimeMeta）。
const encodeJSONExample = `{
  "text": "https://example.com/",                              // 实际编码的内容
  "format": "json",                                            // 输出格式，固定 json
  "version": 0,                                                // 码格版本 0/1/2，v0 最小
  "level": "Q",                                                // 实际纠错等级（容量够时会升级）
  "levelIndex": 2,                                             // 等级下标 0=L 1=M 2=Q 3=H
  "mask": 14,                                                  // 实际掩码 0~31
  "typebits": 122236,                                          // 18 位格式信息位
  "mode": "ALPHANUMERIC",                                      // 数据模式
  "transformed": "5M +*-F/2IH%%Q53E9:/-IP",                    // 仅字母数字模式：码内变换文本
  "codewords": 38,                                             // 码字总数（数据+纠错）
  "ecCodewords": 18,                                           // 其中纠错码字数
  "size": 600,                                                 // 位图边长（像素）
  "svg": "<svg width=\"600\" height=\"600\" …>（完整 SVG 文本）",
  "pngBase64": "iVBORw0KGgoAAAANSUhEUg…（PNG 的 base64，不含 data: 前缀）"
}`

// batchReqExample POST /api/encode/batch 的请求体示例。
const batchReqExample = `{
  "format": "json",                 // 公共格式：建议 json，可一次拿到 svg + pngBase64
  "size": 300,                      // 公共边长，120~2000
  "margin": 0,                      // 公共白边
  "items": [                        // 必填，1~50 条，逐条独立处理
    { "text": "https://a.example.com/1" },   // 第 1 条
    { "text": "https://b.example.com/2" }    // 第 2 条（可用 format/size/logo 覆盖顶层）
  ]
}`

// batchRespExample 批量生成的响应示例（format=json 时 svg + pngBase64 同时给出）。
const batchRespExample = `{
  "ok": 1,                       // 成功条数
  "failed": 1,                   // 失败条数
  "total": 2,                    // 总条数 = ok + failed
  "charged": 1,                  // 实际扣的额度（示例/重复/失败不计）
  "results": [                   // 逐条结果，顺序与 items 一致
    {
      "index": 0,                                  // 对应 items 下标，从 0 开始
      "text": "https://a.example.com/1",           // 该条内容
      "ok": true,                                  // 该条是否成功
      "version": 0,                                // 该条码格版本
      "level": "M",                                // 该条实际纠错等级
      "mask": 7,                                   // 该条实际掩码
      "mode": "ALPHANUMERIC",                      // 该条数据模式
      "svg": "<svg width=\"300\" height=\"300\" …>",  // 该条 SVG（format=svg/json）
      "pngBase64": "iVBORw0KGgoAAAANSUhEUg…",       // 该条图片 base64（非 svg）
      "bytes": 32313,                              // 该条图片字节数
      "ms": 19                                     // 该条耗时（毫秒）
    },
    {
      "index": 1,                                  // 第 2 条
      "text": "https://b.example.com/2",
      "ok": false,                                 // 失败
      "error": "内容过长（上限 8192 字节）"  // 失败原因
    }
  ]
}`

// decodeReqExample 解析接口的 JSON 投喂方式（也可用 multipart 或原始图片字节）。
const decodeReqExample = `{
  "image": "data:image/png;base64,iVBORw0KGgo…",   // 必填其一：data URL / 裸 base64 / http(s) 图片地址
  // 也可以只给图片地址："url": "https://example.com/code.png"
  // 或者根本不发 JSON：直接 multipart -F 'file=@code.png'，或把图片字节当 body 发
}`

// decodeRespExample 太阳码解析结果（真实值：600×600 的 https://example.com/ 码）。
const decodeRespExample = `{
  "family": "WX_CODE",           // 码型：WX_CODE / QR_CODE（auto 时）
  "version": 0,                  // 太阳码 0~2
  "ecLevel": "Q",                // 纠错等级 L/M/Q/H
  "mask": 14,                    // 掩码 0~31
  "typebits": 122236,            // 18 位格式信息位
  "mode": "ALPHANUMERIC",        // 数据模式
  "count": 23,                   // 声明的字符数
  "qrText": "5M +*-F/2IH%%Q53E9:/-IP",   // 仅字母数字模式：码内变换文本
  "payload": "https://example.com/",     // 解出的最终内容（业务只用这个）
  "corrections": 0,              // RS 纠错修掉的码字数，0 表示很干净
  "box": {                       // 码在图中的位置（0~1 归一化），定位失败不返回
    "x": 0.499,                  // 圆心横坐标
    "y": 0.499,                  // 圆心纵坐标
    "rx": 0.507,                 // 横向半径
    "ry": 0.507                  // 纵向半径
  }
}`

// qrRespExample 普通二维码（微信好友名片）解析结果。
const qrRespExample = `{
  "text": "https://u.wechat.com/xxxxxxxxxxxx",   // 二维码里的完整文本
  "url": "https://u.wechat.com/xxxxxxxxxxxx",    // 其中的链接，可直接给 /api/encode
  "version": 4,                                  // QR 版本 1~40
  "ecLevel": "M",                                // 纠错等级
  "mask": 3,                                     // 掩码 0~7
  "mode": "BYTE",                                // 数据模式
  "modules": 33,                                 // 模块边长 = 17 + 4 × version
  "corrections": 0                               // RS 修掉的码字数
}`

// encodeJSONFields 是 format=json 时的响应字段（对应 encode.go 的 runtimeMeta）。
var encodeJSONFields = []apiFieldDoc{
	apiField("text", "string", "实际编码的内容，与请求的 text 一致"),
	apiField("format", "string", "本次输出格式，固定 json"),
	apiField("version", "integer", "码格版本 0 / 1 / 2，v0 最小"),
	apiField("level", "string", "实际纠错等级 L / M / Q / H，容量够时会高于请求的 level"),
	apiField("levelIndex", "integer", "纠错等级下标：0=L、1=M、2=Q、3=H"),
	apiField("mask", "integer", "实际掩码 0~31；请求 mask=-1 时按评分自动选优"),
	apiField("typebits", "integer", "18 位格式信息位（版本 + 纠错等级 + 掩码的编码）"),
	apiField("mode", "string", "数据模式：NUMERIC 纯数字 / ALPHANUMERIC 字母数字 / BYTE 字节"),
	apiField("transformed", "string", "仅 ALPHANUMERIC 出现：真正写进码里的变换文本"),
	apiField("codewords", "integer", "码字总数（数据码字 + 纠错码字）"),
	apiField("ecCodewords", "integer", "其中的纠错码字数"),
	apiField("size", "integer", "位图边长（像素），与请求的 size 一致"),
	apiField("svg", "string", "完整 SVG 文本，可直接另存为 .svg"),
	apiField("pngBase64", "string", "PNG 的 base64（不含 data: 前缀），配合 data:image/png;base64, 可预览"),
	apiField("logoAnimated", "boolean", "仅 logo 是动图时出现，固定 true"),
	apiField("logoFrames", "integer", "仅 logo 是动图时出现：动图帧数"),
	apiField("badgeAnimated", "boolean", "仅徽标是动图时出现，固定 true"),
	apiField("badgeFrames", "integer", "仅徽标是动图时出现：动图帧数"),
}

// decodeFields 是解析结果的字段说明，覆盖太阳码 / 普通二维码两种码型的并集。
var decodeFields = []apiFieldDoc{
	apiField("family", "string", "码型：WX_CODE 太阳码；family=auto 时也可能是 QR_CODE"),
	apiField("version", "integer", "版本号：太阳码 0~2，二维码 1~40"),
	apiField("ecLevel", "string", "纠错等级 L / M / Q / H"),
	apiField("mask", "integer", "掩码编号：太阳码 0~31，二维码 0~7"),
	apiField("typebits", "integer", "太阳码的 18 位格式信息位；QR_CODE 时没有该字段"),
	apiField("mode", "string", "数据模式 NUMERIC / ALPHANUMERIC / BYTE；二维码多段时形如 ALPHANUMERIC+BYTE"),
	apiField("count", "integer", "数据段声明的字符数；QR_CODE 时没有该字段"),
	apiField("qrText", "string", "仅 ALPHANUMERIC 出现：码内承载的变换文本，调试用"),
	apiField("payload", "string", "解出的最终内容（已做逆变换），业务只需用这个字段"),
	apiField("corrections", "integer", "RS 纠错修掉的码字数，0 表示原图很干净"),
	apiField("box", "object", "码在图中的外接圆（归一化 0~1）；定位失败时整个字段不返回"),
	apiField("box.x", "number", "圆心横坐标：0 左边缘、1 右边缘"),
	apiField("box.y", "number", "圆心纵坐标：0 上边缘、1 下边缘"),
	apiField("box.rx", "number", "横向半径，按图片宽度归一化"),
	apiField("box.ry", "number", "纵向半径，按图片高度归一化"),
	apiField("text", "string", "仅 QR_CODE 出现：二维码里的完整文本"),
	apiField("url", "string", "仅 QR_CODE 出现：文本中的链接，没有链接则不存在"),
	apiField("modules", "integer", "仅 QR_CODE 出现：边长模块数 = 17 + 4 × version"),
}

// qrFields 是普通二维码解析结果的字段说明。
var qrFields = []apiFieldDoc{
	apiField("text", "string", "二维码里的完整文本"),
	apiField("url", "string", "文本中的 http(s) 链接；没有链接则不存在，可直接丢给 /api/encode"),
	apiField("version", "integer", "QR 版本 1~40"),
	apiField("ecLevel", "string", "纠错等级 L / M / Q / H"),
	apiField("mask", "integer", "掩码 0~7"),
	apiField("mode", "string", "数据模式 NUMERIC / ALPHANUMERIC / BYTE，多段时用 + 连接"),
	apiField("modules", "integer", "边长模块数 = 17 + 4 × version"),
	apiField("corrections", "integer", "RS 纠错修掉的码字数，0 表示原图很干净"),
}

// batchFields 是批量生成响应的字段说明（数组元素用 results[].xxx 表示）。
var batchFields = []apiFieldDoc{
	apiField("ok", "integer", "成功的条数"),
	apiField("failed", "integer", "失败的条数"),
	apiField("total", "integer", "提交的总条数（= ok + failed）"),
	apiField("charged", "integer", "本次实际扣掉的额度条数；示例 / 重复 / 失败的内容不计"),
	apiField("results", "array", "逐条结果，顺序与请求的 items 一致"),
	apiField("results[].index", "integer", "对应请求 items 的下标，从 0 开始"),
	apiField("results[].text", "string", "该条要编码的内容"),
	apiField("results[].ok", "boolean", "该条是否成功；失败的条目其余字段都不出现"),
	apiField("results[].error", "string", "失败原因；成功时不出现"),
	apiField("results[].version", "integer", "该条的码格版本"),
	apiField("results[].level", "string", "该条实际纠错等级"),
	apiField("results[].mask", "integer", "该条实际掩码"),
	apiField("results[].mode", "string", "该条数据模式"),
	apiField("results[].svg", "string", "该条 SVG 文本；format=svg 或 json 时才有"),
	apiField("results[].pngBase64", "string", "该条图片的 base64；format 不是 svg 时才有"),
	apiField("results[].bytes", "integer", "该条图片字节数；format=svg 时不出现"),
	apiField("results[].ms", "integer", "该条耗时（毫秒）"),
}

// encodeBodyFields 是 POST /api/encode 的请求字段。
// OpenAPI 的 requestBody schema、Markdown 文档的参数表都从这里生成，避免两处描述走样。
func encodeBodyFields() []apiBodyDoc {
	return []apiBodyDoc{
		apiBody("text", "json", "string", fmt.Sprintf("要编码的内容，必填。接口上限 %d 字节；但一枚码只能装几十字节（URL 约 58 字符、中文按字节约 44 字节），超了返回 400", netutil.MaxTextLen), true, "https://example.com/"),
		apiBody("format", "json", "string", "输出格式，默认 png。png/jpg/gif 返回图片二进制，svg 返回矢量文本，json 返回结构体", false, "png", "png", "jpg", "svg", "gif", "json"),
		apiBody("size", "json", "integer", fmt.Sprintf("位图边长（像素），%d~%d，默认 %d；对 svg 无效", minSize, maxSize, defaultSize), false, 600),
		apiBody("margin", "json", "integer", fmt.Sprintf("位图四周白边（像素），0~%d，默认 0", maxMargin), false, 0),
		apiBody("fg", "json", "string", "前景色（码点颜色），#rrggbb，默认 #000000", false, "#000000"),
		apiBody("bg", "json", "string", "背景色，#rrggbb，默认 #ffffff", false, "#ffffff"),
		apiBody("level", "json", "string", "允许的最低纠错等级，默认 L；容量够时自动取更高等级（响应里的 level 是实际值）", false, "L", "L", "M", "Q", "H"),
		apiBody("version", "json", "string", "码格版本：auto（默认，取最小可容纳）/ 0 / 1 / 2（钉死版本，装不下会报错）", false, "auto", "auto", "0", "1", "2"),
		apiBody("mask", "json", "integer", "掩码：-1 自动在 0~31 里按评分选最优，也可固定 0~31", false, -1),
		apiBody("logo", "json", "string", "中心 logo：data URL / http(s) 地址 / 裸 base64 / 空字符串不画；multipart 时也可以传 logo 文件。服务器本地路径需开 ALLOW_LOCAL_IMAGE。GIF 动图 + format=gif 会逐帧保留", false, ""),
		apiBody("logoRatio", "json", "number", fmt.Sprintf("logo 半径缩放，%.1f~%.1f，默认 1（半径 = 108/600 边长）", netutil.MinLogoRatio, netutil.MaxLogoRatio), false, 1),
		apiBody("badge", "json", "string", "角落徽标：default（内嵌图，SVG 写成 data URI、离线可用）/ native（写回远程 URL）/ none（不画）/ 自定义图片地址、data URL 或裸 base64；支持 PNG/JPG/GIF，format=gif 时保留动画，可与中心 logo 同时播放；最多合成 20 帧，超出按完整周期抽帧", false, "default"),
		apiBody("fillBadge", "json", "boolean", "隐藏徽标时用同风格码点填补右下角空位，默认 false；配合 badge=none 使用，显示徽标时忽略", false, false),
		apiBody("style", "json", "string", "笔画风格（只决定形状）：classic（默认，等宽胶囊，与微信原生一致）/ hand（手绘：笔线抖动、粗细不匀、徽标也手贴）/ dot（圆点）/ block（方块）/ line（线描：细环+中心点）。装饰性抖动都控制在半格内，不影响扫码", false, "classic", qrcode.StyleNames...),
		apiBody("color", "json", "string", "配色（只决定墨色，可与任意笔画风格组合）：solid（默认，用 fg）/ rainbow（色环：按半径渐变，内圈青→外圈品红）/ random（彩虹：每一笔随机一色）/ ghost（隐形：墨色只比底色深几级，肉眼几乎看不出，徽标自动隐藏）。除 solid 外都不使用 fg", false, "solid", qrcode.ColorNames...),
		apiBody("ghost", "json", "integer", fmt.Sprintf("隐形配色的深度：墨色比底色深几级（2~40，默认 %d）。越小越隐形、越难扫：2~4 基本看不见但只有程序能解，8 以上手机更容易扫出来", qrcode.GhostDefault), false, qrcode.GhostDefault),
	}
}

// ---------- 接口清单 ----------

func apiOperations() []apiOperationDoc {
	imageResp := []apiResponseDoc{
		apiRespF("200", "生成成功，返回内容由 format 决定（图片 / SVG / JSON），并带 X-WXCode-* 响应头",
			[]string{"image/png", "image/jpeg", "image/gif", "image/svg+xml", "application/json"},
			encodeJSONExample, encodeJSONFields...),
		apiResp("400", "内容为空 / 超出容量 / 参数非法", []string{"application/json"},
			`{"error":"载荷超出一枚葵花码容量（v0..v2 均装不下，请缩短内容）"}`),
		apiResp("403", "内容命中黑名单关键词，或 logo / url 指向内网地址", []string{"application/json"},
			`{"error":"内容包含禁止发布的词语"}`),
		apiResp("429", "单 IP 超过每分钟调用上限，响应带 Retry-After", []string{"application/json"},
			`{"error":"请求过于频繁，请稍后再试"}`),
		apiResp("503", "同时编码 / 解析的任务已满，请稍后重试", []string{"application/json"},
			`{"error":"服务繁忙，请稍后重试"}`),
	}
	authResp := []apiResponseDoc{
		apiResp("200", "成功", []string{"application/json"}, ""),
		apiResp("400", "参数不合法", []string{"application/json"}, ""),
		apiResp("401", "未登录 / Key 无效", []string{"application/json"}, `{"error":"请先登录","needLogin":true}`),
		apiResp("403", "账号封禁或额度用尽", []string{"application/json"}, ""),
	}
	adminResp := []apiResponseDoc{
		apiResp("200", "成功", []string{"application/json"}, ""),
		apiResp("404", "非管理员访问时统一返回 404", []string{"application/json"}, `{"error":"接口不存在"}`),
	}

	encodeFields := encodeBodyFields()
	var encodeParams []apiParamDoc
	for _, f := range encodeFields {
		encodeParams = append(encodeParams, apiParam(f.Name, "query", f.Type, f.Description, f.Example, f.Enum...))
	}

	settingsBody := []apiBodyDoc{
		apiBody("siteName", "json", "string", "站点名称，≤40 字", false, "葵码生成器"),
		apiBody("registerOpen", "json", "boolean", "是否开放注册", false, true),
		apiBody("inviteRequired", "json", "boolean", "注册是否需要邀请码", false, false),
		apiBody("requireLogin", "json", "boolean", "生成 / 解析是否需要登录", false, false),
		apiBody("defaultQuota", "json", "integer", "新用户默认额度", false, 100),
		apiBody("recordEnabled", "json", "boolean", "是否保存记录", false, true),
		apiBody("recordPayloadMode", "json", "string", "full / hash / none", false, "full", "full", "hash", "none"),
		apiBody("recordRetentionDays", "json", "integer", "记录保留天数，0 永久", false, 30),
		apiBody("siteKeywords", "json", "string", "SEO 关键词", false, ""),
		apiBody("siteAnnounce", "json", "string", "站点公告，支持 HTML（原样注入页面顶部横幅）", false, ""),
		apiBody("footerHtml", "json", "string", "底部统计代码", false, ""),
		apiBody("corsOrigins", "json", "string", "跨域来源白名单，逗号分隔", false, ""),
		apiBody("anonLogoKb", "json", "integer", "匿名 logo 上限 KB", false, 8192),
		apiBody("userLogoKb", "json", "integer", "登录 logo 上限 KB", false, 8192),
		apiBody("anonImageKb", "json", "integer", "匿名解析图片上限 KB", false, 24576),
		apiBody("userImageKb", "json", "integer", "登录解析图片上限 KB", false, 24576),
		apiBody("emailVerifyEnabled", "json", "boolean", "是否开启邮箱验证（需先配置完整 SMTP，否则返回 400）", false, false),
		apiBody("registerEmailRequired", "json", "boolean", "是否强制邮箱注册（注册必须验证邮箱；同样需要 SMTP 完整）", false, false),
		apiBody("smtpHost", "json", "string", "SMTP 服务器，如 smtp.qq.com", false, "smtp.qq.com"),
		apiBody("smtpPort", "json", "integer", "SMTP 端口，1~65535", false, 465),
		apiBody("smtpUser", "json", "string", "SMTP 账号（内网免认证可留空）", false, "noreply@qq.com"),
		apiBody("smtpPass", "json", "string", "SMTP 密码/授权码；留空表示保持原值", false, ""),
		apiBody("smtpPassClear", "json", "boolean", "清除已保存的密码/授权码", false, false),
		apiBody("smtpFrom", "json", "string", "发信地址", false, "noreply@qq.com"),
		apiBody("smtpFromName", "json", "string", "发信人名称，≤60 字", false, "葵码生成器"),
		apiBody("smtpTls", "json", "string", "ssl / starttls / none", false, "ssl", "ssl", "starttls", "none"),
	}

	all := []apiOperationDoc{
		{
			ID: "encodePost", Method: "POST", Path: "/api/encode", Tag: "生成",
			Summary: "生成太阳码（图片 / JSON）", Auth: "optional", Content: "json",
			Description: "输入内容生成葵花码。请求体支持 JSON、普通表单或 multipart（multipart 时 logo 可传文件）；\nPNG / JPG / GIF 返回图片二进制，SVG 返回矢量文本，format=json 返回结构体。",
			Body:        encodeFields, Responses: imageResp,
			RequestExample: encodeReqExample,
			Try: apiJSONTry(
				tryF("text", "body", "内容", "textarea", "https://example.com/"),
				tryF("format", "body", "格式", "select", "png", "png", "jpg", "svg", "gif", "json"),
				tryF("size", "body", "尺寸 px", "number", 600),
				tryF("style", "body", "笔画风格", "select", "classic", qrcode.StyleNames...),
				tryF("color", "body", "配色", "select", "solid", qrcode.ColorNames...),
				tryF("ghost", "body", "隐形深度", "number", qrcode.GhostDefault),
			),
		},
		{
			ID: "encodeGet", Method: "GET", Path: "/api/encode", Tag: "生成",
			Summary: "生成太阳码（query，可直接放进 img）", Auth: "optional", Content: "none",
			Description: "与 POST /api/encode 能力完全一致，只是参数放在 query 里，适合 <img src=\"...\">。\n二进制响应同样带 X-WXCode-* 头。",
			Params:      encodeParams, Responses: imageResp,
			RequestExample: encodeGetExample,
			Try: apiQueryTry(
				tryF("text", "query", "内容", "text", "https://example.com/"),
				tryF("format", "query", "格式", "select", "png", "png", "jpg", "svg", "gif", "json"),
				tryF("size", "query", "尺寸 px", "number", 400),
				tryF("style", "query", "笔画风格", "select", "classic", qrcode.StyleNames...),
				tryF("color", "query", "配色", "select", "solid", qrcode.ColorNames...),
				tryF("ghost", "query", "隐形深度", "number", qrcode.GhostDefault),
			),
		},
		{
			ID: "encodeBatch", Method: "POST", Path: "/api/encode/batch", Tag: "生成",
			Summary: "批量生成太阳码", Auth: "optional", Content: "json",
			Description: "一次最多 50 条，逐条独立、逐条计费。顶层参数作用于全部条目，items[].text 为每条内容，\nitems 里也可以用 format / size / margin / fg / bg / logo / logoRatio / badge / fillBadge / style / color / ghost 覆盖顶层值。",
			Body: []apiBodyDoc{
				apiBody("items", "json", "array", "内容数组，每项至少要有 text，可逐条覆盖公共参数", true, []any{
					map[string]any{"text": "https://a.example.com/1"},
					map[string]any{"text": "https://b.example.com/2"},
				}),
				apiBody("format", "json", "string", "公共输出格式，默认 png；建议用 json 以便一次拿到 svg + pngBase64", false, "json", "png", "jpg", "svg", "gif", "json"),
				apiBody("size", "json", "integer", "公共位图边长，120~2000，默认 600", false, 300),
				apiBody("margin", "json", "integer", "公共白边，0~200，默认 0", false, 0),
				apiBody("fg", "json", "string", "公共前景色 #rrggbb", false, "#000000"),
				apiBody("bg", "json", "string", "公共背景色 #rrggbb", false, "#ffffff"),
				apiBody("level", "json", "string", "允许的最低纠错等级", false, "L", "L", "M", "Q", "H"),
				apiBody("version", "json", "string", "auto / 0 / 1 / 2", false, "auto", "auto", "0", "1", "2"),
				apiBody("mask", "json", "integer", "-1 自动，0~31 固定", false, -1),
				apiBody("logo", "json", "string", "公共 logo：data URL / http(s) / 裸 base64", false, ""),
				apiBody("logoRatio", "json", "number", "logo 半径缩放 0.2~3", false, 1),
				apiBody("badge", "json", "string", "角落徽标 default / native / none / 图片地址", false, "default"),
				apiBody("fillBadge", "json", "boolean", "隐藏徽标时填补空位，默认 false；单条可显式传 false 覆盖公共参数", false, false),
				apiBody("style", "json", "string", "公共笔画风格，默认 classic；可选 dot / block / line / hand", false, "classic", qrcode.StyleNames...),
				apiBody("color", "json", "string", "公共配色：solid（用 fg）/ rainbow（色环）/ random（彩虹）/ ghost（隐形）", false, "solid", qrcode.ColorNames...),
				apiBody("ghost", "json", "integer", "隐形深度 2~40，默认 6", false, qrcode.GhostDefault),
			},
			Responses: []apiResponseDoc{
				apiRespF("200", "逐条结果：整体仍是 200，单条成败看 results[].ok", []string{"application/json"},
					batchRespExample, batchFields...),
				apiResp("400", "items 为空 / 超过 50 条 / JSON 不合法", []string{"application/json"},
					`{"error":"items 不能为空"}`),
				apiResp("413", "请求体超过 4MB 上限", []string{"application/json"},
					`{"error":"请求体超过大小上限"}`),
				apiResp("429", "超过每分钟调用上限", []string{"application/json"},
					`{"error":"请求过于频繁，请稍后再试"}`),
			},
			RequestExample: batchReqExample,
			Try: &apiTryDoc{Mode: "json-editor", Template: `{
  "format": "json",
  "size": 300,
  "items": [
    {"text": "https://a.example.com/1"},
    {"text": "https://b.example.com/2"}
  ]
}`},
		},
		{
			ID: "decode", Method: "POST", Path: "/api/decode", Tag: "解析",
			Summary: "解析太阳码 / 自动识别", Auth: "optional", Content: "json+multipart+binary",
			Description: "上传图片解析太阳码。三种投喂方式任选其一：multipart 字段 file（也接受 image）、原始图片字节、\nJSON 的 image / url。加 family=auto 时先试太阳码再试普通二维码，prefer=qr 可让二维码优先。",
			Params: []apiParamDoc{
				apiParam("family", "query", "string", "传 auto 启用「太阳码 + 普通二维码」互补识别", "auto", "auto"),
				apiParam("prefer", "query", "string", "family=auto 时优先尝试的码型，默认 wx", "qr", "wx", "qr"),
				apiParam("url", "query", "string", "直接给公网图片地址，由服务端下载后解析", ""),
			},
			Body: []apiBodyDoc{
				apiBody("file", "multipart", "string", "图片文件字段，也接受 image / logo / icon", false, nil),
				apiBody("image", "json", "string", "data URL / 裸 base64 / http(s) 图片地址", false, ""),
				apiBody("url", "json", "string", "图片 URL，与 image 二选一（仅公网）", false, ""),
			},
			Responses: []apiResponseDoc{
				apiRespF("200", "识别成功：family 区分码型，payload 是最终内容", []string{"application/json"},
					decodeRespExample, decodeFields...),
				apiResp("400", "没识别到码 / 图片不合法 / 缺字段（family=auto 时错误文案会以「未识别到太阳码或普通二维码」开头）", []string{"application/json"},
					`{"error":"未找到葵花码的 3 组定位图案"}`),
				apiResp("403", "图片地址指向内网 / 回环地址", []string{"application/json"},
					`{"error":"拒绝访问内网 / 回环地址"}`),
				apiResp("429", "超过每分钟调用上限", []string{"application/json"},
					`{"error":"请求过于频繁，请稍后再试"}`),
			},
			RequestExample: decodeReqExample,
			Try:            apiMultipartTry("待解析图片", tryF("family", "query", "family", "select", "auto", "", "auto")),
		},
		{
			ID: "qr", Method: "POST", Path: "/api/qr", Tag: "解析",
			Summary: "解析普通二维码（好友码）", Auth: "optional", Content: "json+multipart+binary",
			Description: "读取普通 QR。微信「加好友」名片会解出 https://u.wechat.com/... 这类链接，\nurl 字段可直接丢给 /api/encode 再生成太阳码。",
			Body: []apiBodyDoc{
				apiBody("file", "multipart", "string", "二维码图片文件（也接受 image）", false, nil),
				apiBody("image", "json", "string", "data URL / 裸 base64 / http(s) 图片地址", false, ""),
				apiBody("url", "json", "string", "图片 URL，与 image 二选一（仅公网）", false, ""),
			},
			Responses: []apiResponseDoc{
				apiRespF("200", "识别成功，返回二维码文本与其中的链接", []string{"application/json"},
					qrRespExample, qrFields...),
				apiResp("400", "没找到定位图案 / 校验失败 / 图片不合法", []string{"application/json"},
					`{"error":"没找到二维码的定位图案"}`),
				apiResp("429", "超过每分钟调用上限", []string{"application/json"},
					`{"error":"请求过于频繁，请稍后再试"}`),
			},
			RequestExample: decodeReqExample,
			Try:            apiMultipartTry("二维码图片"),
		},
		{
			ID: "register", Method: "POST", Path: "/api/register", Tag: "账户",
			Summary: "注册并自动登录", Auth: "none", Content: "json",
			Description: "是否开放注册、是否需要邀请码、是否必须验证邮箱由后台配置决定；成功后下发会话 Cookie。\n开启「强制邮箱注册」时，email 与 emailCode 必填（验证码通过 /api/register/email/send 获取）。",
			Body: []apiBodyDoc{
				apiBody("username", "json", "string", "用户名 3~32 位", true, "demo_user"),
				apiBody("password", "json", "string", "密码至少 6 位", true, "secret123"),
				apiBody("inviteCode", "json", "string", "邀请码（开启时必填）", false, ""),
				apiBody("email", "json", "string", "邮箱（开启强制邮箱注册时必填）", false, "me@example.com"),
				apiBody("emailCode", "json", "string", "邮箱验证码（开启强制邮箱注册时必填）", false, "123456"),
			},
			Responses: authResp,
			Try: apiJSONTry(
				tryF("username", "body", "用户名", "text", "demo_user"),
				tryF("password", "body", "密码", "password", "secret123"),
				tryF("inviteCode", "body", "邀请码（可选）", "text", ""),
				tryF("email", "body", "邮箱（可选）", "text", "me@example.com"),
				tryF("emailCode", "body", "邮箱验证码（可选）", "text", ""),
			),
		},
		{
			ID: "registerEmailSend", Method: "POST", Path: "/api/register/email/send", Tag: "账户",
			Summary: "注册邮箱验证码", Auth: "none", Content: "json",
			Description: "仅当「开放注册 + 强制邮箱注册 + 发信配置完整」时可用；邮箱已被其他账号绑定时直接拒绝。\n验证码 6 位、10 分钟有效，同一邮箱 60 秒内不能重发、每小时最多 5 条。",
			Body: []apiBodyDoc{
				apiBody("email", "json", "string", "待验证的邮箱", true, "me@example.com"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "验证码已发送（邮箱已脱敏）", []string{"application/json"}, `{"ok":true,"email":"me***@example.com","expiresIn":600,"retryIn":60}`),
				apiResp("400", "邮箱格式错误 / 已被占用 / 未开启邮箱注册", []string{"application/json"}, ""),
				apiResp("403", "管理员已关闭注册", []string{"application/json"}, ""),
				apiResp("429", "发送过于频繁", []string{"application/json"}, ""),
				apiResp("503", "已开启邮箱注册但发信配置不完整", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(tryF("email", "body", "邮箱", "text", "me@example.com")),
		},
		{
			ID: "login", Method: "POST", Path: "/api/login", Tag: "账户",
			Summary: "登录", Auth: "none", Content: "json",
			Description: "用户名密码登录，成功后下发 HttpOnly 会话 Cookie。\n已绑定邮箱的账号也可以用邮箱登录（忽略大小写与首尾空格）；同一邮箱关联多个账号时返回 409，需改用用户名。",
			Body: []apiBodyDoc{
				apiBody("username", "json", "string", "用户名或已绑定邮箱", true, "admin"),
				apiBody("password", "json", "string", "密码", true, "secret123"),
			},
			Responses: authResp,
			Try: apiJSONTry(
				tryF("username", "body", "用户名", "text", "admin"),
				tryF("password", "body", "密码", "password", "secret123"),
			),
		},
		{
			ID: "logout", Method: "POST", Path: "/api/logout", Tag: "账户",
			Summary: "退出登录", Auth: "optional", Content: "none",
			Description: "清除当前会话 Cookie，幂等。", Responses: authResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "me", Method: "GET", Path: "/api/me", Tag: "账户",
			Summary: "当前账号与前台配置", Auth: "optional", Content: "none",
			Description: "返回当前用户、额度、API Key（仅本人）和公开配置；匿名时 user 为 null。",
			Responses:   authResp, Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "mePassword", Method: "POST", Path: "/api/me/password", Tag: "账户",
			Summary: "修改自己的密码", Auth: "user", Content: "json",
			Description: "修改密码后会踢掉其它会话。",
			Body: []apiBodyDoc{
				apiBody("oldPassword", "json", "string", "原密码", true, ""),
				apiBody("newPassword", "json", "string", "新密码至少 6 位", true, ""),
			},
			Responses: authResp,
			Try: apiJSONTry(
				tryF("oldPassword", "body", "原密码", "password", ""),
				tryF("newPassword", "body", "新密码", "password", ""),
			),
		},
		{
			ID: "meKey", Method: "POST", Path: "/api/me/key", Tag: "账户",
			Summary: "刷新 API Key", Auth: "user", Content: "none",
			Description: "旧 Key 立即失效，返回新 Key。", Responses: authResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "meRecords", Method: "GET", Path: "/api/me/records", Tag: "账户",
			Summary: "我的生成 / 解析记录", Auth: "user", Content: "none",
			Description: "按类型筛选自己的记录，分页返回。",
			Params: []apiParamDoc{
				apiParam("kind", "query", "string", "gen / decode", "gen", "gen", "decode"),
				apiParam("limit", "query", "integer", "每页条数，默认 20，最大 500", 20),
				apiParam("offset", "query", "integer", "偏移量，默认 0", 0),
			},
			Responses: authResp,
			Try: apiQueryTry(
				tryF("kind", "query", "类型", "select", "", "", "gen", "decode"),
				tryF("limit", "query", "每页条数", "number", 20),
				tryF("offset", "query", "偏移量", "number", 0),
			),
		},
		{
			ID: "meRecordsExport", Method: "GET", Path: "/api/me/records/export", Tag: "账户",
			Summary: "导出我的记录 CSV", Auth: "user", Content: "none",
			Description: "导出上限 10000 条，列包含时间、类型、来源、内容、规格。",
			Responses:   []apiResponseDoc{apiResp("200", "CSV 文件", []string{"text/csv"}, "")},
			Try:         &apiTryDoc{Mode: "none"},
		},
		{
			ID: "meRecordDelete", Method: "DELETE", Path: "/api/me/records/{id}", Tag: "账户",
			Summary: "删除我的一条记录", Auth: "user", Content: "none",
			Description: "只能删除自己的记录。",
			Params:      []apiParamDoc{apiParam("id", "path", "integer", "记录 ID", 1)},
			Responses:   []apiResponseDoc{apiResp("200", "成功", []string{"application/json"}, ""), apiResp("404", "记录不存在或不属于当前用户", []string{"application/json"}, "")},
			Try:         &apiTryDoc{Mode: "none", Fields: []apiTryField{{Name: "id", In: "path", Label: "记录 ID", Type: "number", Value: 1, Required: true}}},
		},
		{
			ID: "meRecordsClear", Method: "DELETE", Path: "/api/me/records", Tag: "账户",
			Summary: "清空我的全部记录", Auth: "user", Content: "none",
			Description: "只删除自己的记录，不影响账号和额度。",
			Responses:   []apiResponseDoc{apiResp("200", "成功，返回 deleted 条数", []string{"application/json"}, "")},
			Try:         &apiTryDoc{Mode: "none"},
		},
		{
			ID: "passwordForgot", Method: "POST", Path: "/api/password/forgot", Tag: "账户",
			Summary: "找回密码：发送邮箱验证码", Auth: "none", Content: "json",
			Description: "三件事同时满足才会发信：管理员开启邮箱验证、发信配置完整、该账号已绑定邮箱。\n否则返回 400 / 403 并提示联系管理员；用户不存在与未绑定邮箱返回同一句话，避免被用来枚举用户名。",
			Body: []apiBodyDoc{
				apiBody("username", "json", "string", "用户名", true, "demo_user"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "验证码已发送（邮箱已脱敏）", []string{"application/json"}, `{"ok":true,"email":"de***@example.com","expiresIn":600,"retryIn":60}`),
				apiResp("400", "账号未绑定邮箱（或用户名不存在）", []string{"application/json"}, `{"error":"该账号未绑定邮箱，无法通过邮箱找回，请联系管理员重置密码"}`),
				apiResp("403", "邮箱验证未开启 / 发信配置不完整 / 账号被封禁", []string{"application/json"}, ""),
				apiResp("429", "发送过于频繁（60 秒冷却 / 每小时上限 / 单 IP 限速）", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(tryF("username", "body", "用户名", "text", "demo_user")),
		},
		{
			ID: "passwordReset", Method: "POST", Path: "/api/password/reset", Tag: "账户",
			Summary: "找回密码：验证码重置", Auth: "none", Content: "json",
			Description: "校验邮箱验证码并把密码改成 newPassword（至少 6 位）。\n成功后该账号的全部会话立即失效，验证码一次性作废。",
			Body: []apiBodyDoc{
				apiBody("username", "json", "string", "用户名", true, "demo_user"),
				apiBody("code", "json", "string", "6 位邮箱验证码", true, "123456"),
				apiBody("newPassword", "json", "string", "新密码，至少 6 位", true, "newpass123"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "密码已重置", []string{"application/json"}, `{"ok":true}`),
				apiResp("400", "验证码错误/过期、密码过短、未绑定邮箱", []string{"application/json"}, ""),
				apiResp("403", "邮箱验证未开启 / 发信配置不完整 / 账号被封禁", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(
				tryF("username", "body", "用户名", "text", "demo_user"),
				tryF("code", "body", "邮箱验证码", "text", "123456"),
				tryF("newPassword", "body", "新密码", "password", "newpass123"),
			),
		},
		{
			ID: "meEmailSend", Method: "POST", Path: "/api/me/email/send", Tag: "账户",
			Summary: "绑定邮箱：发送验证码", Auth: "user", Content: "json",
			Description: "给待绑定邮箱发 6 位验证码；邮箱已被其他账号占用时直接拒绝。",
			Body: []apiBodyDoc{
				apiBody("email", "json", "string", "待绑定的邮箱", true, "me@example.com"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "验证码已发送", []string{"application/json"}, `{"ok":true,"email":"me***@example.com","expiresIn":600,"retryIn":60}`),
				apiResp("400", "邮箱格式不正确 / 已被其他账号绑定 / 与当前邮箱相同", []string{"application/json"}, ""),
				apiResp("403", "邮箱验证未开启或发信配置不完整", []string{"application/json"}, ""),
				apiResp("429", "发送过于频繁", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(tryF("email", "body", "邮箱", "text", "me@example.com")),
		},
		{
			ID: "meEmailBind", Method: "POST", Path: "/api/me/email/bind", Tag: "账户",
			Summary: "绑定邮箱：提交验证码", Auth: "user", Content: "json",
			Description: "验证码校验通过后写入 users.email，返回最新的用户信息。",
			Body: []apiBodyDoc{
				apiBody("email", "json", "string", "待绑定的邮箱", true, "me@example.com"),
				apiBody("code", "json", "string", "6 位验证码", true, "123456"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "绑定成功", []string{"application/json"}, `{"ok":true,"user":{}}`),
				apiResp("400", "验证码错误/过期 / 邮箱已被占用", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(
				tryF("email", "body", "邮箱", "text", "me@example.com"),
				tryF("code", "body", "验证码", "text", "123456"),
			),
		},
		{
			ID: "meEmailUnbind", Method: "POST", Path: "/api/me/email/unbind", Tag: "账户",
			Summary: "解绑邮箱", Auth: "user", Content: "json",
			Description: "需要当前密码确认；解绑后该账号无法再走邮箱找回密码。",
			Body: []apiBodyDoc{
				apiBody("password", "json", "string", "当前密码", true, ""),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "已解绑", []string{"application/json"}, `{"ok":true,"user":{}}`),
				apiResp("403", "密码不正确", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(tryF("password", "body", "当前密码", "password", "")),
		},
		{
			ID: "adminEmailTest", Method: "POST", Path: "/api/admin/email/test", Tag: "管理",
			Summary: "发送测试邮件", Auth: "admin", Content: "json",
			Description: "用传入的表单值试发一封（不落库）；密码留空则使用已保存的授权码。",
			Body: []apiBodyDoc{
				apiBody("to", "json", "string", "收件邮箱，留空用发信地址", false, "me@example.com"),
				apiBody("host", "json", "string", "临时覆盖 SMTP 服务器", false, ""),
				apiBody("port", "json", "integer", "临时覆盖端口", false, 465),
				apiBody("user", "json", "string", "临时覆盖账号", false, ""),
				apiBody("pass", "json", "string", "临时覆盖密码/授权码", false, ""),
				apiBody("from", "json", "string", "临时覆盖发信地址", false, ""),
				apiBody("fromName", "json", "string", "临时覆盖发信人名称", false, ""),
				apiBody("tls", "json", "string", "ssl / starttls / none", false, "ssl"),
			},
			Responses: []apiResponseDoc{
				apiResp("200", "发送成功", []string{"application/json"}, `{"ok":true,"to":"me@example.com","ms":320}`),
				apiResp("400", "配置不完整或邮箱格式错误", []string{"application/json"}, ""),
				apiResp("502", "SMTP 连接/认证/投递失败", []string{"application/json"}, ""),
			},
			Try: apiJSONTry(
				tryF("to", "body", "收件邮箱", "text", "me@example.com"),
			),
		},
		{
			ID: "adminOverview", Method: "GET", Path: "/api/admin/overview", Tag: "管理",
			Summary: "后台概览统计与配置", Auth: "admin", Content: "none",
			Description: "仅管理员可访问，非管理员一律 404。", Responses: adminResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "adminSettingsGet", Method: "GET", Path: "/api/admin/settings", Tag: "管理",
			Summary: "读取全部配置", Auth: "admin", Content: "none",
			Description: "返回当前配置快照。", Responses: adminResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "adminSettingsPut", Method: "PUT", Path: "/api/admin/settings", Tag: "管理",
			Summary: "更新站点配置", Auth: "admin", Content: "json",
			Description: "只更新传入的字段，保存后立即生效。", Body: settingsBody, Responses: adminResp,
			Try: &apiTryDoc{Mode: "json-editor", Template: `{
  "siteName": "葵码生成器",
  "registerOpen": true,
  "requireLogin": false,
  "recordEnabled": true,
  "recordPayloadMode": "full",
  "recordRetentionDays": 30
}`},
		},
		{
			ID: "adminIconUpload", Method: "POST", Path: "/api/admin/icon", Tag: "管理",
			Summary: "上传站点图标", Auth: "admin", Content: "multipart",
			Description: "自动居中裁方、缩到 256×256 PNG；也接受原始图片字节或 JSON data URL。",
			Body:        []apiBodyDoc{apiBody("icon", "multipart", "string", "图片文件，也接受 file / image", false, nil)},
			Responses:   adminResp, Try: apiMultipartTry("图标图片"),
		},
		{
			ID: "adminIconDelete", Method: "DELETE", Path: "/api/admin/icon", Tag: "管理",
			Summary: "恢复默认图标", Auth: "admin", Content: "none",
			Description: "删除自定义图标并回到内置图。", Responses: adminResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "adminUsers", Method: "GET", Path: "/api/admin/users", Tag: "管理",
			Summary: "用户列表", Auth: "admin", Content: "none",
			Description: "按用户名 / 邮箱 / 备注搜索，按状态筛选。",
			Params: []apiParamDoc{
				apiParam("q", "query", "string", "搜索关键词", ""),
				apiParam("status", "query", "string", "active / banned", "", "active", "banned"),
				apiParam("limit", "query", "integer", "默认 50，最大 200", 50),
				apiParam("offset", "query", "integer", "默认 0", 0),
			},
			Responses: adminResp,
			Try: apiQueryTry(
				tryF("q", "query", "搜索", "text", ""),
				tryF("status", "query", "状态", "select", "", "", "active", "banned"),
			),
		},
		{
			ID: "adminUserCreate", Method: "POST", Path: "/api/admin/users", Tag: "管理",
			Summary: "后台创建用户", Auth: "admin", Content: "json",
			Description: "注册关闭时也能建号。",
			Body: []apiBodyDoc{
				apiBody("username", "json", "string", "用户名", true, "demo_user"),
				apiBody("password", "json", "string", "密码至少 6 位", true, "secret123"),
				apiBody("email", "json", "string", "邮箱", false, ""),
				apiBody("note", "json", "string", "备注", false, ""),
				apiBody("quota", "json", "integer", "初始额度", false, 100),
				apiBody("role", "json", "string", "user / admin", false, "user", "user", "admin"),
			},
			Responses: adminResp,
			Try: apiJSONTry(
				tryF("username", "body", "用户名", "text", "demo_user"),
				tryF("password", "body", "密码", "password", "secret123"),
				tryF("quota", "body", "额度", "number", 100),
			),
		},
		{
			ID: "adminUserUpdate", Method: "PUT", Path: "/api/admin/users/{id}", Tag: "管理",
			Summary: "修改用户额度 / 状态", Auth: "admin", Content: "json",
			Description: "可改额度、状态、邮箱、备注；封禁后会话立即失效。",
			Params:      []apiParamDoc{apiParam("id", "path", "integer", "用户 ID", 1)},
			Body: []apiBodyDoc{
				apiBody("quota", "json", "integer", "剩余额度", false, 100),
				apiBody("status", "json", "string", "active / banned", false, "active", "active", "banned"),
				apiBody("email", "json", "string", "邮箱", false, ""),
				apiBody("note", "json", "string", "备注", false, ""),
			},
			Responses: adminResp,
			Try: apiJSONTry(
				tryF("id", "path", "用户 ID", "number", 1),
				tryF("quota", "body", "额度", "number", 100),
				tryF("status", "body", "状态", "select", "active", "active", "banned"),
			),
		},
		{
			ID: "adminUserPassword", Method: "POST", Path: "/api/admin/users/{id}/password", Tag: "管理",
			Summary: "重置用户密码", Auth: "admin", Content: "json",
			Description: "管理员直接重置指定用户的密码。",
			Params:      []apiParamDoc{apiParam("id", "path", "integer", "用户 ID", 1)},
			Body:        []apiBodyDoc{apiBody("password", "json", "string", "新密码至少 6 位", true, "secret123")},
			Responses:   adminResp,
			Try: apiJSONTry(
				tryF("id", "path", "用户 ID", "number", 1),
				tryF("password", "body", "新密码", "password", "secret123"),
			),
		},
		{
			ID: "adminRecords", Method: "GET", Path: "/api/admin/records", Tag: "管理",
			Summary: "全部生成 / 解析记录", Auth: "admin", Content: "none",
			Description: "可按用户、类型过滤，随响应返回保留策略摘要。",
			Params: []apiParamDoc{
				apiParam("userId", "query", "integer", "用户 ID，0 为不限", 0),
				apiParam("kind", "query", "string", "gen / decode", "", "gen", "decode"),
				apiParam("limit", "query", "integer", "默认 50，最大 500", 50),
				apiParam("offset", "query", "integer", "默认 0", 0),
			},
			Responses: adminResp,
			Try: apiQueryTry(
				tryF("kind", "query", "类型", "select", "", "", "gen", "decode"),
				tryF("limit", "query", "条数", "number", 50),
			),
		},
		{
			ID: "adminRecordsPurge", Method: "POST", Path: "/api/admin/records/purge", Tag: "管理",
			Summary: "手动清理过期记录", Auth: "admin", Content: "none",
			Description: "不带 before 时按当前保留天数清理；永久保留配置下必须显式给 before。",
			Params:      []apiParamDoc{apiParam("before", "query", "string", "删除该日期之前的记录，如 2026-01-01", "")},
			Responses:   adminResp,
			Try:         &apiTryDoc{Mode: "query", Fields: []apiTryField{{Name: "before", In: "query", Label: "before", Type: "text", Value: "", Placeholder: "2026-01-01"}}},
		},
		{
			ID: "adminInvites", Method: "GET", Path: "/api/admin/invites", Tag: "管理",
			Summary: "邀请码列表", Auth: "admin", Content: "none",
			Description: "返回全部邀请码与使用次数。", Responses: adminResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "adminInviteCreate", Method: "POST", Path: "/api/admin/invites", Tag: "管理",
			Summary: "创建邀请码", Auth: "admin", Content: "json",
			Description: "code 留空时自动生成 8 位邀请码。",
			Body: []apiBodyDoc{
				apiBody("code", "json", "string", "邀请码 4~32 位，可留空", false, ""),
				apiBody("maxUses", "json", "integer", "最多使用次数", false, 1),
				apiBody("expiresAt", "json", "string", "过期时间 YYYY-MM-DD 或 RFC3339", false, ""),
				apiBody("note", "json", "string", "备注", false, ""),
			},
			Responses: adminResp,
			Try: apiJSONTry(
				tryF("code", "body", "邀请码（可空）", "text", ""),
				tryF("maxUses", "body", "可用次数", "number", 1),
			),
		},
		{
			ID: "adminInviteDelete", Method: "DELETE", Path: "/api/admin/invites/{code}", Tag: "管理",
			Summary: "删除邀请码", Auth: "admin", Content: "none",
			Description: "按邀请码删除。",
			Params:      []apiParamDoc{apiParam("code", "path", "string", "邀请码", "ABCD1234")},
			Responses:   adminResp,
			Try:         &apiTryDoc{Mode: "none", Fields: []apiTryField{{Name: "code", In: "path", Label: "邀请码", Type: "text", Value: "ABCD1234", Required: true}}},
		},
		{
			ID: "adminBlacklist", Method: "GET", Path: "/api/admin/blacklist", Tag: "管理",
			Summary: "黑名单列表", Auth: "admin", Content: "none",
			Description: "关键词与 IP 两类规则。", Responses: adminResp,
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "adminBlacklistCreate", Method: "POST", Path: "/api/admin/blacklist", Tag: "管理",
			Summary: "新增黑名单", Auth: "admin", Content: "json",
			Description: "kind=keyword 禁生成内容；kind=ip 拒绝该来源访问。",
			Body: []apiBodyDoc{
				apiBody("kind", "json", "string", "keyword / ip", true, "keyword", "keyword", "ip"),
				apiBody("value", "json", "string", "关键词或 IP", true, "example"),
				apiBody("note", "json", "string", "备注", false, ""),
			},
			Responses: adminResp,
			Try: apiJSONTry(
				tryF("kind", "body", "类型", "select", "keyword", "keyword", "ip"),
				tryF("value", "body", "内容", "text", "example"),
				tryF("note", "body", "备注", "text", ""),
			),
		},
		{
			ID: "adminBlacklistUpdate", Method: "PUT", Path: "/api/admin/blacklist/{id}", Tag: "管理",
			Summary: "修改黑名单", Auth: "admin", Content: "json",
			Description: "修改内容与备注。",
			Params:      []apiParamDoc{apiParam("id", "path", "integer", "规则 ID", 1)},
			Body: []apiBodyDoc{
				apiBody("kind", "json", "string", "keyword / ip", true, "keyword", "keyword", "ip"),
				apiBody("value", "json", "string", "关键词或 IP", true, "example"),
				apiBody("note", "json", "string", "备注", false, ""),
			},
			Responses: adminResp,
			Try: apiJSONTry(
				tryF("id", "path", "规则 ID", "number", 1),
				tryF("kind", "body", "类型", "select", "keyword", "keyword", "ip"),
				tryF("value", "body", "内容", "text", "example"),
			),
		},
		{
			ID: "adminBlacklistDelete", Method: "DELETE", Path: "/api/admin/blacklist/{id}", Tag: "管理",
			Summary: "删除黑名单", Auth: "admin", Content: "none",
			Description: "按 ID 删除一条规则。",
			Params:      []apiParamDoc{apiParam("id", "path", "integer", "规则 ID", 1)},
			Responses:   adminResp,
			Try:         &apiTryDoc{Mode: "none", Fields: []apiTryField{{Name: "id", In: "path", Label: "规则 ID", Type: "number", Value: 1, Required: true}}},
		},
		{
			ID: "health", Method: "GET", Path: "/api/health", Tag: "系统",
			Summary: "健康检查", Auth: "none", Content: "none",
			Description: "带一次数据库 Ping；数据库不可用时返回 503。",
			Responses: []apiResponseDoc{
				apiResp("200", "服务正常", []string{"application/json"}, `{"ok":true,"service":"wxcode","version":"`+config.Version+`","db":"ok"}`),
				apiResp("503", "数据库不可用", []string{"application/json"}, `{"ok":false,"db":"unavailable"}`),
			},
			Try: &apiTryDoc{Mode: "none"},
		},
		{
			ID: "docsMarkdown", Method: "GET", Path: "/api/docs.md", Tag: "系统",
			Summary: "Markdown 接口文档", Auth: "none", Content: "none",
			Description: "默认附件下载；加 inline=1 浏览器直接预览。",
			Params:      []apiParamDoc{apiParam("inline", "query", "string", "传 1 直接预览", "1")},
			Responses:   []apiResponseDoc{apiResp("200", "Markdown 文档", []string{"text/markdown"}, "")},
			Try:         &apiTryDoc{Mode: "query", Fields: []apiTryField{{Name: "inline", In: "query", Label: "inline", Type: "select", Value: "1", Options: []string{"", "1"}}}},
		},
		{
			ID: "openapi", Method: "GET", Path: "/api/openapi.json", Tag: "系统",
			Summary: "OpenAPI 3 文档", Auth: "none", Content: "none",
			Description: "标准 OpenAPI 3.0.3，可导入 Swagger UI、Apifox 等工具；download=1 作为附件下载。",
			Params:      []apiParamDoc{apiParam("download", "query", "string", "传 1 下载文件", "1")},
			Responses:   []apiResponseDoc{apiResp("200", "OpenAPI JSON", []string{"application/json"}, "")},
			Try:         &apiTryDoc{Mode: "query", Fields: []apiTryField{{Name: "download", In: "query", Label: "download", Type: "select", Value: "1", Options: []string{"", "1"}}}},
		},
		{
			ID: "postman", Method: "GET", Path: "/api/postman.json", Tag: "系统",
			Summary: "Postman Collection 下载", Auth: "none", Content: "none",
			Description: "Postman Collection v2.1，导入后把 baseUrl / apiKey 变量改成自己的即可。",
			Params:      []apiParamDoc{apiParam("inline", "query", "string", "传 1 浏览器预览", "1")},
			Responses:   []apiResponseDoc{apiResp("200", "Postman Collection JSON", []string{"application/json"}, "")},
			Try:         &apiTryDoc{Mode: "query", Fields: []apiTryField{{Name: "inline", In: "query", Label: "inline", Type: "select", Value: "1", Options: []string{"", "1"}}}},
		},
		{
			ID: "badge", Method: "GET", Path: "/badge.png", Tag: "系统",
			Summary: "内置角落徽标", Auth: "none", Content: "none",
			Description: "返回内置 PNG 徽标，适合相对地址引用。",
			Responses:   []apiResponseDoc{apiResp("200", "PNG 图片", []string{"image/png"}, "")},
			Try:         &apiTryDoc{Mode: "none"},
		},
		{
			ID: "icon", Method: "GET", Path: "/icon.png", Tag: "系统",
			Summary: "站点图标", Auth: "none", Content: "none",
			Description: "自定义图标或内置默认图；/favicon.ico 与 /apple-touch-icon.png 同源。",
			Responses:   []apiResponseDoc{apiResp("200", "PNG 图片", []string{"image/png"}, "")},
			Try:         &apiTryDoc{Mode: "none"},
		},
	}

	// 公开接口只保留「生成 / 解析」。账户、管理、系统接口仍供站内页面使用，
	// 但不进入接口页、OpenAPI、Postman 和 Markdown 文档，避免对外披露。
	publicPaths := map[string]bool{
		"/api/encode":       true,
		"/api/encode/batch": true,
		"/api/decode":       true,
		"/api/qr":           true,
	}
	out := make([]apiOperationDoc, 0, 5)
	for _, op := range all {
		if publicPaths[op.Path] {
			out = append(out, op)
		}
	}
	return out
}

// ---------- OpenAPI ----------

func openAPISecurity(auth string) []any {
	apiKey := map[string]any{"ApiKey": []string{}}
	cookie := map[string]any{"SessionCookie": []string{}}
	switch auth {
	case "none":
		return []any{}
	case "optional":
		return []any{map[string]any{}, apiKey, cookie}
	default:
		return []any{apiKey, cookie}
	}
}

func openAPISchema(fields []apiBodyDoc, only string) (map[string]any, []string) {
	props := map[string]any{}
	var required []string
	for _, f := range fields {
		if f.In != only {
			continue
		}
		prop := map[string]any{"type": f.Type, "description": f.Description}
		if f.Format != "" {
			prop["format"] = f.Format
		}
		if len(f.Enum) > 0 {
			prop["enum"] = f.Enum
		}
		if f.Example != nil && f.Example != "" {
			prop["example"] = f.Example
		}
		if f.Type == "array" {
			prop["items"] = map[string]any{"type": "object"}
		}
		if only == "multipart" && f.Type == "string" {
			prop["format"] = "binary"
		}
		props[f.Name] = prop
		if f.Required {
			required = append(required, f.Name)
		}
	}
	return props, required
}

// publicAPITags 只输出实际存在的公开分组，顺序跟接口清单一致。
func publicAPITags(ops []apiOperationDoc) []any {
	desc := map[string]string{
		"生成": "生成太阳码图片或 JSON",
		"解析": "太阳码、普通二维码识别",
	}
	seen := map[string]bool{}
	out := make([]any, 0, 2)
	for _, op := range ops {
		if seen[op.Tag] {
			continue
		}
		seen[op.Tag] = true
		out = append(out, map[string]any{"name": op.Tag, "description": desc[op.Tag]})
	}
	return out
}

func buildOpenAPI(baseURL string) map[string]any {
	st := store.LoadSettings()
	paths := map[string]any{}
	ops := apiOperations()
	for order, op := range ops {
		item := map[string]any{
			"tags":          []string{op.Tag},
			"summary":       op.Summary,
			"description":   op.Description,
			"operationId":   op.ID,
			"x-order":       order,
			"x-wxcode-auth": op.Auth,
			"x-wxcode-try":  op.Try,
			"responses":     buildOpenAPIResponses(op.Responses),
		}
		if op.RequestExample != "" {
			// 带 // 注释的示例，接口页直接展示（注释已对齐）
			item["x-wxcode-request-example"] = jsoncAlign(op.RequestExample)
		}
		if sec := openAPISecurity(op.Auth); !config.APIOnly() && len(sec) > 0 {
			item["security"] = sec
		}
		if config.APIOnly() {
			item["x-wxcode-auth"] = "none"
		}
		if len(op.Params) > 0 {
			params := make([]any, 0, len(op.Params))
			for _, p := range op.Params {
				schema := map[string]any{"type": p.Type}
				if len(p.Enum) > 0 {
					schema["enum"] = p.Enum
				}
				if p.Example != nil && p.Example != "" {
					schema["example"] = p.Example
				}
				params = append(params, map[string]any{
					"name": p.Name, "in": p.In, "required": p.Required || p.In == "path",
					"description": p.Description, "schema": schema,
				})
			}
			item["parameters"] = params
		}
		if body := buildOpenAPIRequestBody(op); body != nil {
			item["requestBody"] = body
		}
		path, _ := paths[op.Path].(map[string]any)
		if path == nil {
			path = map[string]any{}
			paths[op.Path] = path
		}
		path[strings.ToLower(op.Method)] = item
	}

	description := "太阳码（WX_CODE）生成、解析与账号管理接口。右侧的 Try it 可直接在浏览器里发请求。"
	if config.APIOnly() {
		description = "纯 API 模式：太阳码生成与解析，无需登录或 API Key。"
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       st.SiteName + " API",
			"version":     config.Version,
			"description": description,
		},
		"servers": []any{map[string]any{"url": baseURL, "description": "当前站点"}},
		"tags":    publicAPITags(ops),
		"paths":   paths,
		"components": map[string]any{
			"schemas": map[string]any{
				"Error": map[string]any{
					"type": "object", "properties": map[string]any{"error": map[string]any{"type": "string"}},
				},
			},
			"securitySchemes": map[string]any{
				"ApiKey": map[string]any{
					"type": "apiKey", "in": "header", "name": "X-API-Key",
					"description": "在「我的」页面复制；也接受 Authorization: Bearer 或 ?key=。",
				},
				"SessionCookie": map[string]any{
					"type": "apiKey", "in": "cookie", "name": store.SessionCookie,
					"description": "网页登录后自动携带。",
				},
			},
		},
		"x-wxcode-version": config.Version,
		// 所有接口都会回写这个头：服务端日志里的 request_id 与之对应，
		// 报障时把它一起提供，就能直接定位到那一条访问日志。
		"x-wxcode-request-id": "X-Request-ID",
	}
}

func buildOpenAPIRequestBody(op apiOperationDoc) map[string]any {
	if op.Content == "none" || len(op.Body) == 0 {
		return nil
	}
	content := map[string]any{}
	jsonProps, jsonReq := openAPISchema(op.Body, "json")
	if len(jsonProps) > 0 {
		schema := map[string]any{"type": "object", "properties": jsonProps}
		if len(jsonReq) > 0 {
			schema["required"] = jsonReq
		}
		media := map[string]any{"schema": schema}
		// 请求体示例也用同一份 JSONC：Swagger UI / Apifox 会直接填进 "Example Value"
		if v, ok := jsoncValue(op.RequestExample); ok {
			media["example"] = v
			media["examples"] = map[string]any{
				"默认": map[string]any{"summary": "最小可用示例", "value": v},
			}
		}
		content["application/json"] = media
	}
	formProps, formReq := openAPISchema(op.Body, "multipart")
	if len(formProps) > 0 {
		schema := map[string]any{"type": "object", "properties": formProps}
		if len(formReq) > 0 {
			schema["required"] = formReq
		}
		content["multipart/form-data"] = map[string]any{"schema": schema}
	}
	if op.Content == "json+multipart+binary" {
		content["application/octet-stream"] = map[string]any{
			"schema": map[string]any{"type": "string", "format": "binary"},
		}
	}
	if len(content) == 0 {
		return nil
	}
	return map[string]any{"content": content}
}

func buildOpenAPIResponses(responses []apiResponseDoc) map[string]any {
	out := map[string]any{}
	for _, r := range responses {
		item := map[string]any{"description": r.Description}
		schema := map[string]any{"type": "object"}
		if len(r.Fields) > 0 {
			schema = schemaFromFields(r.Fields)
			// 接口页用这份扁平列表渲染「响应字段」表格（数组元素写作 results[].index）
			item["x-wxcode-fields"] = r.Fields
		}
		value, hasExample := jsoncValue(r.Example)
		if hasExample {
			// 只有 {"error":"..."} 一个字段时指向公共 Error schema，其他泛化为 object
			if m, ok := value.(map[string]any); ok && len(m) == 1 {
				if _, isErr := m["error"]; isErr {
					schema = map[string]any{"$ref": "#/components/schemas/Error"}
				}
			}
		}
		if len(r.Types) > 0 {
			content := map[string]any{}
			for _, ct := range r.Types {
				if ct == "application/json" {
					media := map[string]any{"schema": schema}
					if hasExample {
						media["example"] = value
						// 带中文注释的版本，接口页直接展示（标准 example 不能带注释）
						media["x-wxcode-example"] = jsoncAlign(r.Example)
					}
					content[ct] = media
					continue
				}
				format := "binary"
				if strings.HasPrefix(ct, "text/") {
					format = ""
				}
				s := map[string]any{"type": "string"}
				if format != "" {
					s["format"] = format
				}
				content[ct] = map[string]any{"schema": s}
			}
			item["content"] = content
		}
		out[r.Code] = item
	}
	return out
}

// ---------- Postman Collection v2.1 ----------

func buildPostmanCollection(baseURL string) map[string]any {
	ops := apiOperations()
	groups := map[string][]any{}
	tagOrder := []string{}
	for _, op := range ops {
		if _, ok := groups[op.Tag]; !ok {
			tagOrder = append(tagOrder, op.Tag)
		}
		groups[op.Tag] = append(groups[op.Tag], buildPostmanItem(op))
	}
	items := make([]any, 0, len(tagOrder))
	for _, tag := range tagOrder {
		items = append(items, map[string]any{"name": tag, "item": groups[tag]})
	}
	return map[string]any{
		"info": map[string]any{
			"name": store.LoadSettings().SiteName + " API",
			"description": "太阳码生成 / 解析接口（与 /api/docs.md、/api/openapi.json 同源）。\n" +
				"导入后先把 baseUrl 改成自己的站点，并按需填写 apiKey；\n" +
				"每个请求都带「请求示例」，右侧还能直接看到各状态码的「保存的响应示例」。",
			"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		"variable": []any{
			map[string]any{"key": "baseUrl", "value": baseURL, "description": "站点地址，例如 https://your.domain"},
			map[string]any{"key": "apiKey", "value": "", "description": "可选。「我的」页面复制的 Key；本站开启「使用需要登录」时必填"},
		},
		"item": items,
	}
}

func buildPostmanItem(op apiOperationDoc) map[string]any {
	rawPath := op.Path
	pathVars := []any{}
	query := []any{}
	addQuery := func(key, val string) {
		if val == "" {
			return
		}
		for _, q := range query {
			if q.(map[string]any)["key"] == key {
				return
			}
		}
		query = append(query, map[string]any{"key": key, "value": val})
	}
	for _, p := range op.Params {
		if p.In == "path" {
			rawPath = strings.ReplaceAll(rawPath, "{"+p.Name+"}", "{{"+p.Name+"}}")
			val := ""
			if p.Example != nil {
				val = fmt.Sprint(p.Example)
			}
			pathVars = append(pathVars, map[string]any{"key": p.Name, "value": val})
		}
		if p.In == "query" {
			val := ""
			if p.Example != nil {
				val = fmt.Sprint(p.Example)
			}
			addQuery(p.Name, val)
		}
	}
	if op.Try != nil {
		for _, f := range op.Try.Fields {
			if f.In == "path" {
				rawPath = strings.ReplaceAll(rawPath, "{"+f.Name+"}", "{{"+f.Name+"}}")
				found := false
				for _, v := range pathVars {
					if v.(map[string]any)["key"] == f.Name {
						found = true
					}
				}
				if !found {
					pathVars = append(pathVars, map[string]any{"key": f.Name, "value": fmt.Sprint(f.Value)})
				}
			}
			if f.In == "query" {
				addQuery(f.Name, fmt.Sprint(f.Value))
			}
		}
	}
	headers := []any{}
	if op.Auth != "none" {
		headers = append(headers, map[string]any{
			"key": "X-API-Key", "value": "{{apiKey}}", "type": "text",
		})
	}
	if op.Content == "json" {
		headers = append(headers, map[string]any{
			"key": "Content-Type", "value": "application/json", "type": "text",
		})
	}

	request := map[string]any{
		"method": op.Method,
		"header": headers,
		"url": map[string]any{
			"raw":   "{{baseUrl}}" + rawPath,
			"query": query,
		},
		"description": postmanDescription(op),
	}
	if len(pathVars) > 0 {
		request["url"].(map[string]any)["variable"] = pathVars
	}
	if body := buildPostmanBody(op); body != nil {
		request["body"] = body
	}
	return map[string]any{
		"name":     op.Summary,
		"request":  request,
		"response": buildPostmanResponses(op, request),
	}
}

// postmanDescription 给每个请求写清「做什么 / 怎么鉴权 / 请求示例」。
// 请求示例和接口页、OpenAPI 是同一份 JSONC（这里去掉注释，保留对齐后的排版）。
func postmanDescription(op apiOperationDoc) string {
	var b strings.Builder
	b.WriteString(op.Description)
	b.WriteString("\n\n鉴权：" + authLabel(op.Auth))
	b.WriteString("\nContent-Type：" + op.Content)
	if op.RequestExample != "" {
		if _, ok := jsoncValue(op.RequestExample); ok {
			b.WriteString("\n\n请求示例（application/json）：\n")
			b.WriteString(jsoncStrip(op.RequestExample))
		} else {
			b.WriteString("\n\n请求示例：\n")
			b.WriteString(strings.ReplaceAll(op.RequestExample, "{{BASE_URL}}", "{{baseUrl}}"))
		}
	}
	return b.String()
}

// buildPostmanResponses 把文档里的响应示例存成 Postman 的「保存的示例」，
// 导入后直接能在右侧看到每个状态码的返回长什么样，不用先发一次请求。
func buildPostmanResponses(op apiOperationDoc, request map[string]any) []any {
	out := []any{}
	for _, r := range op.Responses {
		if _, ok := jsoncValue(r.Example); !ok {
			continue
		}
		body := jsoncStrip(r.Example)
		status := "OK"
		code := 200
		if _, err := fmt.Sscanf(r.Code, "%d", &code); err != nil {
			code = 200
		}
		if code >= 400 {
			status = http.StatusText(code)
			if status == "" {
				status = "Error"
			}
		}
		out = append(out, map[string]any{
			"name":                     op.Summary + " · " + r.Code + " " + r.Description,
			"originalRequest":          request,
			"status":                   status,
			"code":                     code,
			"_postman_previewlanguage": "json",
			"header": []any{
				map[string]any{"key": "Content-Type", "value": "application/json; charset=utf-8"},
			},
			"cookie": []any{},
			"body":   string(body),
		})
	}
	return out
}

func buildPostmanBody(op apiOperationDoc) map[string]any {
	if op.Content == "none" || len(op.Body) == 0 {
		return nil
	}
	if op.Content == "multipart" || op.Content == "json+multipart+binary" {
		form := []any{}
		for _, f := range op.Body {
			if f.In == "multipart" {
				form = append(form, map[string]any{
					"key": f.Name, "type": "file", "src": []any{},
					"description": f.Description,
				})
			}
		}
		// decode / qr 还支持 JSON 投喂，补一行文字说明，避免导入后只会传文件
		desc := "multipart 上传图片；也支持原始图片字节（Content-Type: image/*）"
		if op.Content == "json+multipart+binary" {
			desc += "，或 JSON 的 {\"image\":\"data:image/png;base64,...\"} / {\"url\":\"https://...\"}"
		}
		return map[string]any{"mode": "formdata", "formdata": form, "description": desc}
	}
	// 优先用文档里的请求示例，保证 Postman 与接口页、OpenAPI 完全一致。
	// 用 jsoncStrip 而不是 unmarshal→marshal：后者会把字段按字母序重排，
	// 而示例里的 text / format / size 顺序是刻意排的，读起来更顺。
	raw := ""
	if _, ok := jsoncValue(op.RequestExample); ok {
		raw = jsoncStrip(op.RequestExample)
	}
	if raw == "" {
		example := map[string]any{}
		for _, f := range op.Body {
			if f.In == "json" && f.Example != nil {
				example[f.Name] = f.Example
			}
		}
		if len(example) == 0 {
			example = nil
		}
		if example != nil {
			b, _ := json.MarshalIndent(example, "", "  ")
			raw = string(b)
		}
	}
	if raw == "" {
		raw = "{}"
	}
	return map[string]any{
		"mode": "raw",
		"raw":  raw,
		"options": map[string]any{
			"raw": map[string]any{"language": "json"},
		},
	}
}

func authLabel(auth string) string {
	switch auth {
	case "none":
		return "公开"
	case "optional":
		return "可选 API Key / 会话 Cookie"
	case "user":
		return "需要登录或 API Key"
	case "admin":
		return "需要管理员会话或管理员 API Key"
	}
	return auth
}

// ---------- HTTP ----------

func writeAPISpec(w http.ResponseWriter, r *http.Request, body any, filename string, download bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if download {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = w.Write(data)
}

// handleOpenAPI GET /api/openapi.json
func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.WriteErr(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
		return
	}
	writeAPISpec(w, r, buildOpenAPI(httpx.RequestBaseURL(r)), "wxcode-openapi.json",
		r.URL.Query().Get("download") != "")
}

// handlePostman GET /api/postman.json
func handlePostman(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.WriteErr(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
		return
	}
	writeAPISpec(w, r, buildPostmanCollection(httpx.RequestBaseURL(r)), "wxcode.postman_collection.json",
		r.URL.Query().Get("inline") == "")
}
