package config

import (
	"os"
	"time"
)

// shutdownTimeout 优雅关闭等待在途请求结束的最长时间。
// 容器编排的 stop_grace_period 要比它大，否则编排会先 SIGKILL。
const ShutdownTimeout = 5 * time.Second

// APIOnly reports whether the service runs without the web and management features.
func APIOnly() bool { return EnvTrue("API_ONLY") }

// listenAddr 监听地址：ADDR 优先（如 ":8080"），其次 PORT，默认 :8080。
func ListenAddr() string {
	if a := os.Getenv("ADDR"); a != "" {
		return a
	}
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}
