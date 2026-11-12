package store

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"wxcode/internal/config"
	"wxcode/internal/model"
	"wxcode/internal/watermark"
)

// ---------- 配置项 ----------
// 键名与默认值都集中在这里，改默认值只改这一处。
const SetRegisterOpen = "register_open" // 是否允许注册

var defaultSettings = map[string]string{
	SetRegisterOpen:        "1",   // 默认：开放注册
	SetInviteRequired:      "0",   // 默认：不需要邀请码
	SetRequireLogin:        "0",   // 默认：不登录也能用（生成/解码/好友码）
	SetCaptchaEnabled:      "1",   // 默认：登录 / 注册需要验证码
	SetDefaultQuota:        "100", // 默认：每人 100 次
	SetSiteName:            "葵码生成器",
	SetSiteIcon:            "",
	SetSiteIconVer:         "",
	SetFooterHTML:          "",
	SetSiteKeywords:        "",
	SetSiteAnnounce:        "",
	SetCORSOrigins:         "",
	SetRecordRetentionDays: "0",
	SetRecordEnabled:       "1",
	SetRecordPayloadMode:   "full",
	SetAnonLogoKB:          "8192",  // 8MB
	SetUserLogoKB:          "8192",  // 8MB
	SetAnonImageKB:         "24576", // 24MB
	SetUserImageKB:         "24576", // 24MB
	// 邮箱验证默认关闭：没有发信服务器时开启只会让用户看到「发送失败」。
	// 管理员配好 SMTP 并验证通过后再打开，开启时才允许找回密码走邮箱。
	SetEmailVerify: "0",
	// 强制邮箱注册默认关闭：保持现有注册流程不变（老用户与既有注册行为都不受影响）。
	SetRegisterMail: "0",
	SetSMTPHost:     "",
	SetSMTPPort:     "465",
	SetSMTPUser:     "",
	SetSMTPPass:     "",
	SetSMTPFrom:     "",
	SetSMTPFromName: "",
	SetSMTPTLS:      "ssl",

	// 水印默认「无水印」；管理员在后台挑一档全局配置并填好默认文字后才会生效。
	SetWatermarkMode:     model.WatermarkNone,
	SetWatermarkText:     "",
	SetWatermarkSize:     "20",
	SetWatermarkColor:    "#1f2937",
	SetWatermarkOpacity:  "25",
	SetWatermarkPosition: "center",
}

func GetSetting(k, def string) string {
	var v string
	if err := DB.QueryRow(`SELECT v FROM settings WHERE k = ?`, k).Scan(&v); err != nil {
		return def
	}
	return v
}

// setSetting 写配置。写完立刻让快照失效，下一个请求会重新读全量配置。
func SetSetting(k, v string) error {
	_, err := DB.Exec(`INSERT INTO settings(k, v) VALUES(?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	if err == nil {
		InvalidateSettings()
	}
	return err
}

func SettingBool(k string) bool {
	return settingBoolDefault(k, false)
}

func settingBoolDefault(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(GetSetting(k, "")))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func SettingInt(k string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(GetSetting(k, "")))
	if err != nil {
		return def
	}
	return n
}

// settingsCache 是配置的内存快照。
//
// loadSettings 一次要发十几条 SELECT（一个键一条），实测 155µs —— 比一次编码还慢。
// 而它被挂在了每个请求上（securityHeaders 要判断 CSP 是否放行外链），
// 静态资源也躲不过；叠加 SetMaxOpenConns(1) 之后所有请求都堵在这一条连接上。
// 配置是「读多写极少」的，这里缓存整份快照，写的时候置空即可。
var SettingsCache atomic.Pointer[model.Settings]

// loadSettings 返回配置快照；命中缓存时零查询。
//
// 并发下可能有两个请求同时回源，结果等价，没必要加锁。
func LoadSettings() model.Settings {
	if p := SettingsCache.Load(); p != nil {
		return *p
	}
	s := readSettings()
	if config.APIOnly() {
		s.RecordEnabled = false
		s.CORSOrigins = os.Getenv("API_CORS_ORIGINS")
		s.WatermarkMode = model.WatermarkAll
	}
	SettingsCache.Store(&s)
	return s
}

// NormalizeWatermarkMode 归一化水印全局配置：none / all / user / admin。
//
// 兼容旧值：anon（旧版「谁能自定义」）→ all；未知值回落到 none（最安全的默认：
// 不加水印，至少不会给用户的码糊上一层没人配过的文字）。
func NormalizeWatermarkMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case model.WatermarkAll, "anon", "anonymous", "everyone":
		return model.WatermarkAll
	case model.WatermarkUser, "login", "logged", "user-only":
		return model.WatermarkUser
	case model.WatermarkAdmin, "global":
		return model.WatermarkAdmin
	default:
		return model.WatermarkNone
	}
}

// invalidateSettings 让快照失效（setSetting 写入成功后调用）。
func InvalidateSettings() { SettingsCache.Store(nil) }

// readSettings 从库里读全量配置。只在缓存未命中时执行。
func readSettings() model.Settings {
	icon := GetSetting(SetSiteIcon, "")
	pass := GetSetting(SetSMTPPass, "")
	return model.Settings{
		RegisterOpen:        SettingBool(SetRegisterOpen),
		InviteRequired:      SettingBool(SetInviteRequired),
		RequireLogin:        SettingBool(SetRequireLogin),
		CaptchaEnabled:      settingBoolDefault(SetCaptchaEnabled, true),
		DefaultQuota:        SettingInt(SetDefaultQuota, 100),
		SiteName:            GetSetting(SetSiteName, "葵码生成器"),
		IconVer:             GetSetting(SetSiteIconVer, ""),
		HasIcon:             icon != "",
		IconData:            icon,
		FooterHTML:          GetSetting(SetFooterHTML, ""),
		SiteKeywords:        GetSetting(SetSiteKeywords, ""),
		SiteAnnounce:        GetSetting(SetSiteAnnounce, ""),
		CORSOrigins:         GetSetting(SetCORSOrigins, ""),
		RecordRetentionDays: SettingInt(SetRecordRetentionDays, RetentionDefault),
		RecordEnabled:       settingBoolDefault(SetRecordEnabled, true),
		RecordPayloadMode:   NormalizeRecordPayloadMode(GetSetting(SetRecordPayloadMode, "full")),
		AnonLogoKB:          SettingInt(SetAnonLogoKB, 8192),
		UserLogoKB:          SettingInt(SetUserLogoKB, 8192),
		AnonImageKB:         SettingInt(SetAnonImageKB, 24576),
		UserImageKB:         SettingInt(SetUserImageKB, 24576),

		EmailVerifyEnabled:    SettingBool(SetEmailVerify),
		RegisterEmailRequired: SettingBool(SetRegisterMail),
		SMTPHost:              strings.TrimSpace(GetSetting(SetSMTPHost, "")),
		SMTPPort:              SettingInt(SetSMTPPort, SMTPDefaultPort),
		SMTPUser:              strings.TrimSpace(GetSetting(SetSMTPUser, "")),
		SMTPPass:              pass,
		SMTPPassSet:           pass != "",
		SMTPFrom:              strings.TrimSpace(GetSetting(SetSMTPFrom, "")),
		SMTPFromName:          strings.TrimSpace(GetSetting(SetSMTPFromName, "")),
		SMTPTLS:               model.NormalizeSMTPTLS(GetSetting(SetSMTPTLS, SMTPTLSDefault)),

		WatermarkMode:     NormalizeWatermarkMode(GetSetting(SetWatermarkMode, model.WatermarkNone)),
		WatermarkText:     GetSetting(SetWatermarkText, ""),
		WatermarkSize:     SettingInt(SetWatermarkSize, 20),
		WatermarkColor:    GetSetting(SetWatermarkColor, "#1f2937"),
		WatermarkOpacity:  SettingInt(SetWatermarkOpacity, 25),
		WatermarkPosition: watermark.ParsePosition(GetSetting(SetWatermarkPosition, watermark.PositionCenter)),
	}
}

// publicSettings 是给普通访客/前台的配置：**不含**底部统计代码
// （那玩意是管理员自己填的第三方脚本，没必要下发给每个访客）。
//
// emailResetAvailable 是「开关打开 且 发信配置完整」的结果值：
// 前端只按它决定要不要展示邮箱找回 / 绑定入口，避免开关开了、
// 但 SMTP 没配好时给出一个点了必失败的入口。
func PublicSettings() map[string]any {
	s := LoadSettings()
	return map[string]any{
		"registerOpen":        s.RegisterOpen,
		"inviteRequired":      s.InviteRequired,
		"requireLogin":        s.RequireLogin,
		"captchaEnabled":      s.CaptchaEnabled,
		"defaultQuota":        s.DefaultQuota,
		"siteName":            s.SiteName,
		"hasIcon":             s.HasIcon,
		"iconVer":             s.IconVer,
		"emailVerifyEnabled":  s.EmailVerifyEnabled,
		"smtpConfigured":      s.SMTPReady(),
		"emailResetAvailable": s.EmailResetAvailable(),
		// 水印：是否开启 + 谁能自定义（前端据此决定要不要展示水印设置）
		"watermarkMode":         s.WatermarkMode,
		"registerEmailRequired": s.RegisterEmailRequired,
		// emailRegisterAvailable = 开关打开且发信配置完整；注册页据此决定是否展开邮箱字段
		"emailRegisterAvailable": s.RegisterEmailRequired && s.SMTPReady(),
	}
}

const SMTPDefaultPort = 465

const SMTPTLSDefault = "ssl"

const RetentionDefault = 0
