// Package changelog provides a dynamic, Redis-backed changelog that can be
// updated via API without redeploying. Entries are stored newest-first in a
// Redis list so the docs page always shows the latest changes.
package changelog

import (
	"encoding/json"
	"time"

	"zruvix/internal/redis"
)

const redisKey = "zruvix_changelog"
const maxEntries = 100

// Entry is a single changelog entry.
type Entry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	Changes []string `json:"changes"`
}

// Add prepends a new changelog entry to Redis.
func Add(e Entry) {
	if e.Date == "" {
		e.Date = time.Now().UTC().Format("2006-01-02")
	}
	b, _ := json.Marshal(e)
	redis.LPush(redisKey, string(b))
	redis.LTrim(redisKey, 0, maxEntries-1)
}

// List returns all dynamic changelog entries (newest first).
func List() []Entry {
	raw := redis.LRange(redisKey, 0, maxEntries-1)
	out := make([]Entry, 0, len(raw))
	for _, s := range raw {
		var e Entry
		if json.Unmarshal([]byte(s), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}
