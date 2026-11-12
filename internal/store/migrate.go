package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"wxcode/internal/config"
	"wxcode/internal/logger"
	"wxcode/internal/model"
	"wxcode/internal/passwd"
)

// migrate 建表（IF NOT EXISTS，可重复执行）+ 初始化默认配置与管理员账号。
func migrate() error {
	if _, err := DB.Exec(SchemaSQL); err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}
	// 先做老库迁移，再补默认值：否则 INSERT OR IGNORE 会先把 require_login 写成 0，
	// 旧键 require_login_for_gen 的值就再也迁移不过来了。
	if err := migrateLegacy(); err != nil {
		return err
	}
	// 明文密码 → 哈希（老库升级；已是哈希的行不动）
	if err := migratePasswords(); err != nil {
		return err
	}
	// 旧版「开启水印 + 谁能自定义」→ 四档 watermark_mode
	if err := migrateWatermarkMode(); err != nil {
		return err
	}
	for k, v := range defaultSettings {
		if _, err := DB.Exec(`INSERT OR IGNORE INTO settings(k, v) VALUES(?, ?)`, k, v); err != nil {
			return fmt.Errorf("初始化配置失败: %w", err)
		}
	}
	if config.APIOnly() {
		return nil
	}
	return ensureAdmin()
}

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL,
    password      TEXT    NOT NULL DEFAULT '',   -- 已废弃：仅保留历史列，启动迁移后一律为空
    password_hash TEXT    NOT NULL DEFAULT '',  -- 唯一生效的密码字段：pbkdf2-sha256$iter$salt$hash
    last_ip       TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'user',
    status        TEXT    NOT NULL DEFAULT 'active',
    quota         INTEGER NOT NULL DEFAULT 0,   -- 通用次数，转码/解码都能用
    quota_used    INTEGER NOT NULL DEFAULT 0,
    api_key       TEXT    NOT NULL,
    email         TEXT    NOT NULL DEFAULT '',
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    last_login_at TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username COLLATE NOCASE);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_api_key  ON users(api_key);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
CREATE INDEX IF NOT EXISTS idx_users_email  ON users(email);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    ip         TEXT    NOT NULL DEFAULT '',
    ua         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_exp  ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS settings (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invite_codes (
    code       TEXT    PRIMARY KEY,
    max_uses   INTEGER NOT NULL DEFAULT 1,
    used       INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT    NOT NULL DEFAULT '',
    note       TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    batch_id   TEXT    NOT NULL DEFAULT ''    -- 生成批次（导出本次生成用）
);

-- 邮箱验证码：绑定邮箱 / 找回密码共用。
-- subject 存 user id（字符串），purpose 区分用途；code_hash 是 SHA-256，不落明文。
-- 每个 (purpose, subject) 只保留最新一条，重发时先删旧记录。
CREATE TABLE IF NOT EXISTS email_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    purpose    TEXT    NOT NULL,              -- bind | reset
    subject    TEXT    NOT NULL,              -- 用户 id
    email      TEXT    NOT NULL,              -- 发往哪个邮箱
    code_hash  TEXT    NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_codes_lookup ON email_codes(purpose, subject, id DESC);

-- 卡密：一张卡只能兑换一次，兑换后增加通用次数
-- kind 仅保留给历史数据兼容，新卡统一写 quota
CREATE TABLE IF NOT EXISTS card_keys (
    code       TEXT    PRIMARY KEY,
    kind       TEXT    NOT NULL DEFAULT 'quota',
    amount     INTEGER NOT NULL DEFAULT 1,
    used_by    INTEGER NOT NULL DEFAULT 0,    -- 0 = 未兑换
    used_at    TEXT    NOT NULL DEFAULT '',
    expires_at TEXT    NOT NULL DEFAULT '',
    note       TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    batch_id   TEXT    NOT NULL DEFAULT ''    -- 生成批次（导出本次生成用）
);
CREATE INDEX IF NOT EXISTS idx_card_keys_created ON card_keys(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_card_keys_used    ON card_keys(used_by);

-- 黑名单：kind=keyword 禁止生成内容包含该词；kind=ip 直接拒绝该来源访问
CREATE TABLE IF NOT EXISTS blacklist (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_blacklist_kind_value ON blacklist(kind, value COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_blacklist_kind ON blacklist(kind);

-- 预设 LOGO：管理员上传的中心图，用户在生成页「用预设」一键选用。
-- data 存原始图片字节（GIF 保留动画），随备份 / 导入整体迁移。
-- sort 小的排前面（同值按 id）；enabled=0 只在后台可见。
CREATE TABLE IF NOT EXISTS preset_logos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    mime       TEXT    NOT NULL DEFAULT 'image/png',
    bytes      INTEGER NOT NULL DEFAULT 0,
    sort       INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    data       BLOB    NOT NULL,
    created_at TEXT    NOT NULL
);
-- 索引不在这里建：老库的表可能还没有 sort 列，建索引会先于补列执行而报错，
-- 放到 migrateLegacy 补完列之后再建（见 ensurePresetLogoColumns）。

CREATE TABLE IF NOT EXISTS records (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL DEFAULT 0,
    username   TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    source     TEXT    NOT NULL,
    payload    TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    format     TEXT    NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    ms         INTEGER NOT NULL DEFAULT 0,
    ip         TEXT    NOT NULL DEFAULT '',
    ua         TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_records_user ON records(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_records_time ON records(id DESC);
CREATE INDEX IF NOT EXISTS idx_records_kind ON records(kind, id DESC);
-- hasDone() 的判定条件就是 (user_id, kind, payload)，没有这个索引时只能靠
-- idx_records_user 过滤出一个用户的所有记录再逐条比 payload（表越大越慢）。
CREATE INDEX IF NOT EXISTS idx_records_dedup ON records(user_id, kind, payload);
`

// migrateLegacy 把老库平滑升级到当前结构（可重复执行）：
//   - require_login_for_gen → require_login（配置项改名，沿用原值）
//   - users 增加 password（历史明文列，现已废弃）与 last_ip 两列
func migrateLegacy() error {
	// 配置项改名：仅当新键还没被写过时，继承旧值
	if _, err := DB.Exec(`INSERT INTO settings(k, v)
		SELECT 'require_login', v FROM settings
		WHERE k = 'require_login_for_gen'
		  AND NOT EXISTS (SELECT 1 FROM settings WHERE k = 'require_login')`); err != nil {
		return fmt.Errorf("迁移配置失败: %w", err)
	}
	_, _ = DB.Exec(`DELETE FROM settings WHERE k = 'require_login_for_gen'`)

	if err := ensureColumn("users", "password", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn("users", "last_ip", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn("card_keys", "amount", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := ensureColumn("card_keys", "batch_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn("invite_codes", "batch_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 预设 LOGO 先只有「上传顺序」，后来才加了排序 / 启用两列：
	// 老库补列后按 id 生成 10、20… 的排序值，之后再新增的才能排到最后。
	hadSort, err := columnExists("preset_logos", "sort")
	if err != nil {
		return err
	}
	if err := ensureColumn("preset_logos", "sort", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn("preset_logos", "enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if !hadSort {
		if _, err := DB.Exec(`UPDATE preset_logos SET sort = id * ?`, PresetSortStep); err != nil {
			return fmt.Errorf("初始化预设 LOGO 排序失败: %w", err)
		}
	}
	// 索引必须等 sort 列补好再建，否则老库会在这里炸掉（启动直接失败）
	if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_preset_logos_order ON preset_logos(sort, id)`); err != nil {
		return fmt.Errorf("建立预设 LOGO 排序索引失败: %w", err)
	}
	return nil
}

// columnExists 报告表里是否已有该列。
func columnExists(table, column string) (bool, error) {
	rows, err := DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ensureColumn 给已存在的表补列（SQLite 没有 ADD COLUMN IF NOT EXISTS）。
func ensureColumn(table, column, ddl string) error {
	ok, err := columnExists(table, column)
	if err != nil || ok {
		return err
	}
	_, err = DB.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + ddl)
	return err
}

// migratePasswords 把历史遗留的明文密码转成哈希并清空明文列。
//
// 老库的 users.password 存明文、password_hash 为空。这里逐行转换成
// pbkdf2-sha256 哈希；已经有哈希的行只清理明文列，不动哈希。
// 幂等：转换过的行 password 为空，下次启动不会重复处理。
func migratePasswords() error {
	rows, err := DB.Query(`SELECT id, password, password_hash FROM users WHERE TRIM(password) <> ''`)
	if err != nil {
		return fmt.Errorf("读取历史密码失败: %w", err)
	}
	type legacy struct {
		id    int64
		plain string
		hash  string
	}
	var items []legacy
	for rows.Next() {
		var it legacy
		if err := rows.Scan(&it.id, &it.plain, &it.hash); err != nil {
			rows.Close()
			return fmt.Errorf("读取历史密码失败: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取历史密码失败: %w", err)
	}
	rows.Close()
	if len(items) == 0 {
		return nil
	}

	converted, cleared := 0, 0
	for _, it := range items {
		if strings.TrimSpace(it.hash) != "" {
			// 已有哈希：说明这行是双写状态，以哈希为准，只清明文。
			if _, err := DB.Exec(`UPDATE users SET password = '' WHERE id = ?`, it.id); err != nil {
				return fmt.Errorf("清理历史密码失败: %w", err)
			}
			cleared++
			continue
		}
		hash, err := passwd.Hash(it.plain)
		if err != nil {
			return fmt.Errorf("密码哈希失败: %w", err)
		}
		if _, err := DB.Exec(`UPDATE users SET password = '', password_hash = ?, updated_at = ? WHERE id = ?`,
			hash, NowStr(), it.id); err != nil {
			return fmt.Errorf("写入密码哈希失败: %w", err)
		}
		converted++
	}
	logger.Info("✓ 历史明文密码已转换为哈希", "converted", converted, "cleared", cleared)
	return nil
}

// migrateWatermarkMode 把旧版两项水印配置合并成单一的四档全局配置。
//
// 旧库有 watermark_enabled（0/1）与 watermark_mode（anon/user/admin）：
//
//	enabled=0                → none
//	enabled=1 & mode=anon    → all
//	enabled=1 & mode=user    → user
//	enabled=1 & mode=admin   → admin
//
// 转换后删除 watermark_enabled，避免它继续影响读取；删除后本函数不再命中，幂等。
func migrateWatermarkMode() error {
	var legacy string
	err := DB.QueryRow(`SELECT v FROM settings WHERE k = ?`, SetWatermarkEnabled).Scan(&legacy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取历史水印开关失败: %w", err)
	}

	mode := NormalizeWatermarkMode(GetSetting(SetWatermarkMode, ""))
	enabled := strings.TrimSpace(legacy) == "1" || strings.EqualFold(strings.TrimSpace(legacy), "true")
	if !enabled {
		mode = model.WatermarkNone
	}
	if _, err := DB.Exec(`INSERT INTO settings(k, v) VALUES(?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, SetWatermarkMode, mode); err != nil {
		return fmt.Errorf("迁移水印全局配置失败: %w", err)
	}
	if _, err := DB.Exec(`DELETE FROM settings WHERE k = ?`, SetWatermarkEnabled); err != nil {
		return fmt.Errorf("清理旧水印开关失败: %w", err)
	}
	logger.Info("✓ 水印配置已合并为全局配置", "mode", mode, "legacy_enabled", legacy)
	InvalidateSettings()
	return nil
}
