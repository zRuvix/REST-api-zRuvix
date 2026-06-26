package api

import (
	"net/http"
	"time"

	"zruvix/internal/gateway"
	"zruvix/internal/presence"
	"zruvix/internal/redis"
)

var startedAt = time.Now()

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Redis check
	redisOK := false
	redis.Set("zruvix_health_ping", "1")
	if redis.Get("zruvix_health_ping") == "1" {
		redisOK = true
	}

	respondOK(w, map[string]any{
		"status":          "ok",
		"uptime_ms":       time.Since(startedAt).Milliseconds(),
		"started_at":      startedAt.UnixMilli(),
		"redis_connected": redisOK,
		"gateway_connected": gateway.Connected(),
		"monitored_users": presence.Reg.Count(),
	})
}
