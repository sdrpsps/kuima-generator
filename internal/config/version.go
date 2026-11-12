package config

// Version 版本号，可在构建时注入：-ldflags "-X wxcode/internal/config.Version=1.1.1"
var Version = "1.1.1"

// BuildTime 构建时间，可在构建时注入：-ldflags "-X wxcode/internal/config.BuildTime=2026-01-01T00:00:00Z"
var BuildTime = "unknown"
