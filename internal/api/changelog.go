package api

import (
	"encoding/json"
	"io"
	"net/http"

	"zruvix/internal/changelog"
	"zruvix/internal/redis"
)

// handleGetChangelog returns all dynamic changelog entries (public).
func handleGetChangelog(w http.ResponseWriter, _ *http.Request) {
	respondOK(w, map[string]any{"changelog": changelog.List()})
}

// handlePostChangelog adds a new changelog entry (requires valid API key).
func handlePostChangelog(w http.ResponseWriter, r *http.Request) {
	key := authorizationHeader(r)
	if key == "" || redis.Get("api_key:"+key) == "" {
		noPermission(w)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var e changelog.Entry
	if err := json.Unmarshal(body, &e); err != nil || e.Title == "" || len(e.Changes) == 0 {
		respondError(w, http.StatusBadRequest, "invalid_body", "body must have title and changes[]")
		return
	}

	changelog.Add(e)
	respondOK(w, e)
}
