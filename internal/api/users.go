package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"zruvix/internal/kv"
	"zruvix/internal/presence"
	"zruvix/internal/redis"
)

// usersRouter builds the /v1/users subtree.
func usersRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/@me", handleMe)
	// External now-playing ingest (Pear Desktop, etc.) — API key required.
	r.Put("/@me/now-playing", handlePutMeNowPlaying)
	r.Delete("/@me/now-playing", handleDeleteMeNowPlaying)
	r.Get("/{id}", handleGetUser)
	r.Get("/{id}/history", handleHistory)
	r.Get("/{id}/stats", handleStats)
	r.Get("/{id}/top-tracks", handleTopTracks)
	r.Get("/{id}/card.svg", handleCard)
	r.Put("/{id}/now-playing", handlePutNowPlaying)
	r.Delete("/{id}/now-playing", handleDeleteNowPlaying)
	r.Patch("/{id}/kv", handlePatchKV)
	r.Put("/{id}/kv/{field}", handlePutKV)
	r.Delete("/{id}/kv/{field}", handleDeleteKV)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return r
}

// handleMe resolves the caller via their API key and returns their presence.
func handleMe(w http.ResponseWriter, r *http.Request) {
	key := authorizationHeader(r)
	userID := redis.Get("api_key:" + key)
	if userID == "" {
		noPermission(w)
		return
	}
	respondPresence(w, userID)
}

func handleGetUser(w http.ResponseWriter, r *http.Request) {
	respondPresence(w, chi.URLParam(r, "id"))
}

// handleHistory returns recent status/track history for a user (works offline).
func handleHistory(w http.ResponseWriter, r *http.Request) {
	respondOK(w, presence.History(chi.URLParam(r, "id")))
}

// handleStats returns aggregate presence statistics for a user.
func handleStats(w http.ResponseWriter, r *http.Request) {
	respondOK(w, presence.Stats(chi.URLParam(r, "id")))
}

// handleTopTracks returns a user's most-played tracks (default 10, max 50),
// each with full song details and a play_count. Works offline (reads Redis).
func handleTopTracks(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	respondOK(w, map[string]any{
		"top_tracks": presence.MostPlayed(chi.URLParam(r, "id"), limit),
	})
}

func respondPresence(w http.ResponseWriter, userID string) {
	p, err := presence.GetPrettyPresence(userID)
	if err != nil {
		respondError(w, err.HTTPCode, err.Code, err.Message)
		return
	}
	respondOK(w, p)
}

// handlePatchKV merges a JSON object of key/value pairs into the user's KV.
func handlePatchKV(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if !validateResourceAccess(r, userID) {
		noPermission(w)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		respondError(w, http.StatusNotFound, "invalid_kv_value", "body must be an object")
		return
	}

	pairs := make(map[string]string, len(parsed))
	for k, v := range parsed {
		s, ok := v.(string)
		if !ok {
			// Non-string value: matches the Elixir rescue path.
			respondError(w, http.StatusNotFound, "invalid_kv_value", "body must be an object")
			return
		}
		if verr := kv.ValidatePair(k, s); verr != nil {
			respondError(w, http.StatusNotFound, "kv_validation_failed", verr.Error())
			return
		}
		pairs[k] = s
	}

	if err := kv.Multiset(userID, pairs); err != nil {
		respondError(w, http.StatusNotFound, "kv_validation_failed", err.Error())
		return
	}
	respondNoContent(w)
}

// handlePutKV sets a single key to the request body.
func handlePutKV(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	field := chi.URLParam(r, "field")
	if !validateResourceAccess(r, userID) {
		noPermission(w)
		return
	}

	body, _ := io.ReadAll(r.Body)
	if _, err := kv.Set(userID, field, string(body)); err != nil {
		respondError(w, http.StatusNotFound, "kv_validation_failed", err.Error())
		return
	}
	respondNoContent(w)
}

// handleDeleteKV removes a single key.
func handleDeleteKV(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	field := chi.URLParam(r, "field")
	if !validateResourceAccess(r, userID) {
		noPermission(w)
		return
	}
	_ = kv.Del(userID, field)
	respondNoContent(w)
}

// validateResourceAccess checks the Authorization header maps to userID.
func validateResourceAccess(r *http.Request, userID string) bool {
	key := authorizationHeader(r)
	return redis.Get("api_key:"+key) == userID
}

// resolveAPIKeyUser returns the Discord user id bound to the Authorization key.
func resolveAPIKeyUser(r *http.Request) string {
	key := authorizationHeader(r)
	if key == "" {
		return ""
	}
	return redis.Get("api_key:" + key)
}

// handlePutMeNowPlaying sets external now-playing for the API-key owner.
func handlePutMeNowPlaying(w http.ResponseWriter, r *http.Request) {
	userID := resolveAPIKeyUser(r)
	if userID == "" {
		noPermission(w)
		return
	}
	putNowPlaying(w, r, userID)
}

// handleDeleteMeNowPlaying clears external now-playing for the API-key owner.
func handleDeleteMeNowPlaying(w http.ResponseWriter, r *http.Request) {
	userID := resolveAPIKeyUser(r)
	if userID == "" {
		noPermission(w)
		return
	}
	deleteNowPlaying(w, userID)
}

// handlePutNowPlaying sets external now-playing for :id (must own the API key).
func handlePutNowPlaying(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if !validateResourceAccess(r, userID) {
		noPermission(w)
		return
	}
	putNowPlaying(w, r, userID)
}

// handleDeleteNowPlaying clears external now-playing for :id.
func handleDeleteNowPlaying(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if !validateResourceAccess(r, userID) {
		noPermission(w)
		return
	}
	deleteNowPlaying(w, userID)
}

func putNowPlaying(w http.ResponseWriter, r *http.Request, userID string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid_body", "could not read body")
		return
	}
	var in presence.ExternalMusicInput
	if len(body) == 0 {
		respondError(w, http.StatusBadRequest, "invalid_now_playing", "JSON body required")
		return
	}
	if err := json.Unmarshal(body, &in); err != nil {
		respondError(w, http.StatusBadRequest, "invalid_now_playing", "body must be a JSON object")
		return
	}

	pretty, perr := presence.SetExternalMusic(userID, in)
	if perr != nil {
		respondError(w, perr.HTTPCode, perr.Code, perr.Message)
		return
	}
	respondOK(w, pretty)
}

func deleteNowPlaying(w http.ResponseWriter, userID string) {
	pretty, perr := presence.ClearExternalMusic(userID)
	if perr != nil {
		respondError(w, perr.HTTPCode, perr.Code, perr.Message)
		return
	}
	respondOK(w, pretty)
}
