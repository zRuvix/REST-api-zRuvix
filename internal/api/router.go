package api

import (
	"net/http"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"zruvix/internal/metrics"
	"zruvix/internal/presence"
	"zruvix/internal/socket"
)

// Router builds the full HTTP handler tree, mirroring zRuvix.Api.Router.
func Router() http.Handler {
	r := chi.NewRouter()
	r.Use(corsMiddleware)
	r.Use(metricsMiddleware)

	r.Get("/", handleIndex)
	r.Get("/socket", handleSocket)
	r.Mount("/v1", v1Router())
	r.Get("/discord", handleDiscordRedirect)
	r.Get("/metrics", handleMetrics)

	// Quicklink avatar proxy for /{id}.{ext}, else 404.
	r.Get("/{file}", func(w http.ResponseWriter, req *http.Request) {
		handleQuicklink(w, req, chi.URLParam(req, "file"))
	})
	// Quicklink banner proxy for /banner/{id}.{ext}.
	r.Get("/banner/{file}", func(w http.ResponseWriter, req *http.Request) {
		handleBannerQuicklink(w, req, chi.URLParam(req, "file"))
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { notFound(w) })

	return r
}

func v1Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/docs", handleDocs)
	r.Get("/version", handleVersion)
	r.Get("/health", handleHealth)
	r.Get("/changelog", handleGetChangelog)
	r.Post("/changelog", handlePostChangelog)
	r.Mount("/users", usersRouter())
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return r
}

func handleIndex(w http.ResponseWriter, _ *http.Request) {
	respondOK(w, map[string]any{
		"info":                 "zRuvix exposes your Discord presence and activities as a REST API and WebSocket.",
		"monitored_user_count": presence.Reg.Count(),
		"discord_invite":       "",
	})
}

func handleDiscordRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://discord.gg/rkJRchDy92", http.StatusFound)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics.MonitoredUsers.Set(float64(presence.Reg.Count()))
	metrics.Handler().ServeHTTP(w, r)
}

// handleSocket upgrades the connection to a WebSocket and hands it to the
// socket package. Origins are unrestricted, matching the open CORS policy.
func handleSocket(w http.ResponseWriter, r *http.Request) {
	compression := "json"
	if r.URL.Query().Get("compression") == "zlib_json" {
		compression = "zlib"
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // accept all origins (CORS is "*")
	})
	if err != nil {
		return
	}
	defer ws.CloseNow()

	socket.Handle(r.Context(), ws, compression)
}

// corsMiddleware mirrors the open Corsica config (origins "*", all methods and
// headers) and short-circuits preflight requests with 204.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for metrics while remaining
// transparent to net/http (Unwrap exposes the underlying writer so WebSocket
// hijacking via http.ResponseController still works).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// metricsMiddleware tallies 2xx/4xx/5xx responses, matching metrics_handle.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		switch {
		case rec.status >= 200 && rec.status < 300:
			metrics.Responses2xx.Inc()
		case rec.status >= 400 && rec.status < 500:
			metrics.Responses4xx.Inc()
		case rec.status >= 500:
			metrics.Responses5xx.Inc()
		}
	})
}
