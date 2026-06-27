package presence

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"zruvix/internal/redis"
)

// historyCap is the max number of events retained per list.
const historyCap = 100

// mostPlayedMinPlays is the minimum number of plays a track needs before it
// qualifies for the "most played" list. Set to 1 to include one-time plays.
const mostPlayedMinPlays = 1

// sameTrackReplayMinGapMs is how far a track's start timestamp must move before
// the *same* track id is treated as a genuine replay (a new play) rather than
// the currently-playing song being re-reported. Discord sends one
// PRESENCE_UPDATE per mutual guild and recomputes timestamps.start (= now -
// position) on each event, so the value jitters by up to a few seconds while a
// single song plays continuously. Without this guard every jittered re-report
// was stored as another play, producing duplicate history entries ("0m ago"
// repeated) and inflated play counts. A real replay restarts the track, moving
// start by far more than this threshold.
const sameTrackReplayMinGapMs = 10_000

// absInt64 returns the absolute value of n.
func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// isNewPlay decides whether an incoming track should be recorded as a *new*
// play, given the previously recorded track id and start timestamp.
//
//   - No track playing (newTrack == "") is never a new play.
//   - A different track id is always a new play.
//   - The same track id is only a replay when both starts are known and the new
//     start moved more than sameTrackReplayMinGapMs from the last one (a genuine
//     restart). Smaller moves are timestamp jitter from the same song being
//     re-reported — once per mutual guild — and must be ignored, otherwise one
//     play turns into duplicate history entries and inflated play counts.
func isNewPlay(newTrack, lastTrackID string, newStart, lastTrackStart int64) bool {
	if newTrack == "" {
		return false
	}
	if newTrack != lastTrackID {
		return true
	}
	return newStart != 0 && lastTrackStart != 0 &&
		absInt64(newStart-lastTrackStart) > sameTrackReplayMinGapMs
}

func statusKey(uid string) string   { return "zruvix_hist_status:" + uid }
func tracksKey(uid string) string   { return "zruvix_hist_tracks:" + uid }
func lastSeenKey(uid string) string { return "zruvix_last_seen:" + uid }
func tracksTodayKey(uid string) string {
	return "zruvix_stat_tracks:" + uid + ":" + time.Now().UTC().Format("20060102")
}

// playCountsKey holds a hash of track_id -> total play count for a user.
func playCountsKey(uid string) string { return "zruvix_play_counts:" + uid }

// trackMetaKey holds a hash of track_id -> full now_playing JSON for a user,
// so the most-played list can be enriched with song details (incl. album art).
func trackMetaKey(uid string) string { return "zruvix_track_meta:" + uid }

// trackStart extracts the start timestamp from a NowPlaying's Timestamps map.
func trackStart(np *NowPlaying) int64 {
	if np == nil || np.Timestamps == nil {
		return 0
	}
	if m, ok := np.Timestamps.(map[string]any); ok {
		if s, ok := toInt64(m["start"]); ok {
			return s
		}
	}
	return 0
}

// recordHistory diffs the freshly-built presence against the last seen state and
// appends status-change and track-change events to Redis. It also refreshes the
// last_seen timestamp and the daily track counter.
func recordHistory(p *Presence, pretty *PrettyPresence) {
	p.mu.Lock()
	newStatus := pretty.DiscordStatus
	newTrack := ""
	var newStart int64
	if pretty.NowPlaying != nil && pretty.NowPlaying.TrackID != nil {
		newTrack = *pretty.NowPlaying.TrackID
		newStart = trackStart(pretty.NowPlaying)
	}
	statusChanged := newStatus != p.lastStatus
	trackChanged := isNewPlay(newTrack, p.lastTrackID, newStart, p.lastTrackStart)
	p.lastStatus = newStatus
	if newTrack != "" {
		p.lastTrackID = newTrack
		p.lastTrackStart = newStart
	}
	p.mu.Unlock()

	now := time.Now().UnixMilli()
	redis.Set(lastSeenKey(p.UserID), strconv.FormatInt(now, 10))

	if statusChanged {
		b, _ := json.Marshal(map[string]any{"status": newStatus, "ts": now})
		redis.LPush(statusKey(p.UserID), string(b))
		redis.LTrim(statusKey(p.UserID), 0, historyCap-1)
	}

	if trackChanged && pretty.NowPlaying != nil {
		// Persist the full now_playing object (source, song, artist, album,
		// album_art_url, track_id, track_url, timestamps, duration_ms) so a
		// history entry carries the same data as the live response — including
		// album art for image rendering. We only add the event timestamp.
		event := nowPlayingToMap(pretty.NowPlaying)
		event["ts"] = now
		b, _ := json.Marshal(event)
		redis.LPush(tracksKey(p.UserID), string(b))
		redis.LTrim(tracksKey(p.UserID), 0, historyCap-1)

		// Most-played accounting: bump this track's play count and refresh its
		// stored details so the top-tracks list always has current metadata.
		if np := pretty.NowPlaying; np.TrackID != nil && *np.TrackID != "" {
			redis.HIncrBy(playCountsKey(p.UserID), *np.TrackID, 1)
			meta := nowPlayingToMap(np)
			meta["last_played"] = now
			if mb, err := json.Marshal(meta); err == nil {
				redis.HSet(trackMetaKey(p.UserID), *np.TrackID, string(mb))
			}
		}

		k := tracksTodayKey(p.UserID)
		redis.Incr(k)
		redis.Expire(k, 60*60*48) // keep daily counters ~2 days
	}
}

// History returns recent status-change and track-change events for a user.
// Works even when the user is offline/unmonitored (read straight from Redis).
func History(uid string) map[string]any {
	return map[string]any{
		"status_history": rawList(redis.LRange(statusKey(uid), 0, historyCap-1)),
		"track_history":  rawList(redis.LRange(tracksKey(uid), 0, historyCap-1)),
	}
}

// Stats returns aggregate presence statistics for a user.
func Stats(uid string) map[string]any {
	var lastSeen any
	if v := redis.Get(lastSeenKey(uid)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			lastSeen = n
		}
	}

	tracksToday := 0
	if v := redis.Get(tracksTodayKey(uid)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tracksToday = n
		}
	}

	// Current status: prefer the live registry, fall back to last recorded event.
	currentStatus := "offline"
	if p, ok := Reg.Lookup(uid); ok {
		currentStatus = p.BuildPretty().DiscordStatus
	} else if head := redis.LRange(statusKey(uid), 0, 0); len(head) > 0 {
		var e struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(head[0]), &e) == nil && e.Status != "" {
			currentStatus = e.Status
		}
	}

	return map[string]any{
		"current_status":      currentStatus,
		"last_seen":           lastSeen,
		"tracks_today":        tracksToday,
		"is_monitored":        isMonitored(uid),
		"track_history_count": len(redis.LRange(tracksKey(uid), 0, historyCap-1)),
	}
}

func isMonitored(uid string) bool {
	_, ok := Reg.Lookup(uid)
	return ok
}

// PurgeHistory permanently deletes all of a user's stored presence data:
// status history, listening (track) history, last-seen, today's track counter,
// play counts and the most-played track metadata. Used by the bot's data-
// deletion command so users can control/erase their own data. It does not
// touch KV (handled separately by kv.Clear) or live registry state.
func PurgeHistory(uid string) {
	redis.Del(statusKey(uid))
	redis.Del(tracksKey(uid))
	redis.Del(lastSeenKey(uid))
	redis.Del(tracksTodayKey(uid))
	redis.Del(playCountsKey(uid))
	redis.Del(trackMetaKey(uid))

	// Reset the in-memory diff state so a currently-monitored user starts
	// recording fresh history instead of comparing against pre-purge values.
	if p, ok := Reg.Lookup(uid); ok {
		p.mu.Lock()
		p.lastStatus = ""
		p.lastTrackID = ""
		p.lastTrackStart = 0
		p.mu.Unlock()
	}
}

// nowPlayingToMap flattens a NowPlaying into a map using its JSON field names,
// so the stored history event matches the live now_playing shape (and we can
// then attach extra fields like the event timestamp). Round-tripping through
// json keeps the keys/omitempty behaviour in one place (the struct tags).
func nowPlayingToMap(np *NowPlaying) map[string]any {
	b, _ := json.Marshal(np)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// MostPlayed returns a user's top tracks ranked by play count (highest first),
// each enriched with the full now_playing details plus a play_count field.
// Tracks below mostPlayedMinPlays are excluded; limit is clamped to [1, 50].
func MostPlayed(uid string, limit int) []map[string]any {
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	counts := redis.HGetAll(playCountsKey(uid))

	type ranked struct {
		id    string
		count int64
	}
	list := make([]ranked, 0, len(counts))
	for id, v := range counts {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < mostPlayedMinPlays {
			continue
		}
		list = append(list, ranked{id: id, count: n})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].id < list[j].id // stable tie-break for equal counts
	})
	if len(list) > limit {
		list = list[:limit]
	}

	meta := redis.HGetAll(trackMetaKey(uid))
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		entry := map[string]any{}
		if raw, ok := meta[t.id]; ok && raw != "" {
			_ = json.Unmarshal([]byte(raw), &entry)
		} else {
			entry["track_id"] = t.id
		}
		entry["play_count"] = t.count
		out = append(out, entry)
	}
	return out
}

// rawList wraps stored JSON strings so they embed as objects in the response.
func rawList(items []string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))
	for _, s := range items {
		out = append(out, json.RawMessage(s))
	}
	return out
}
