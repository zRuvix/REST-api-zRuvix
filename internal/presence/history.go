package presence

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

// lastPlayTTL (seconds) is how long the shared last-play marker in Redis is
// kept. It only has to outlive the gap between two updates belonging to the
// same continuous play — including a deploy or crash — so a day is generous and
// costs a single key per user.
const lastPlayTTL = 24 * 60 * 60

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

// playCountsKey holds a hash of track key -> total play count for a user.
func playCountsKey(uid string) string { return "zruvix_play_counts:" + uid }

// trackMetaKey holds a hash of track key -> full now_playing JSON for a user,
// so the most-played list can be enriched with song details (incl. album art).
func trackMetaKey(uid string) string { return "zruvix_track_meta:" + uid }

// lastPlayKey holds the last play recorded for a user, as "<track key>|<start>".
// Unlike the markers on Presence it is shared by every node and survives a
// restart, which is what makes claimPlay able to count a play exactly once.
func lastPlayKey(uid string) string { return "zruvix_last_play:" + uid }

// syntheticTrackKeyPrefix marks a track key we derived ourselves (from source +
// artist + song) because the source gave us no track id. Stored keys carrying it
// are not real Spotify/YouTube ids and must not be surfaced as track_id.
const syntheticTrackKeyPrefix = "h:"

// trackKey returns the identity a track is counted and stored under.
//
// The real track id is preferred, but it is not always there: a YouTube Music
// activity only yields one when Discord sends details_url, and external
// reporters may post nothing but a song and an artist. Those plays used to be
// dropped entirely — no play count, no history entry, no daily counter — even
// though they rendered fine in now_playing. Falling back to a hash of
// source/artist/song keeps them countable and keeps the key stable across the
// plays of one song.
func trackKey(np *NowPlaying) string {
	if np == nil {
		return ""
	}
	if np.TrackID != nil && *np.TrackID != "" {
		return *np.TrackID
	}
	artist := normalizeTrackField(np.Artist)
	song := normalizeTrackField(np.Song)
	if artist == "" && song == "" {
		return ""
	}
	sum := sha1.Sum([]byte(np.Source + "|" + artist + "|" + song))
	return syntheticTrackKeyPrefix + hex.EncodeToString(sum[:])[:16]
}

// normalizeTrackField lowercases and trims a now_playing field (song/artist are
// typed as any because they come straight out of the activity map) so trivial
// formatting differences between reports do not fork one song into two keys.
func normalizeTrackField(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprint(v)
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// claimPlayScript is the cross-node/restart-safe half of new-play detection.
//
// It applies the same rule as isNewPlay against a marker in Redis instead of
// process memory, and does so atomically: a different track is a new play, the
// same track only when both starts are known and differ by more than the jitter
// threshold. Returns 1 for the single caller that may record the play; every
// other caller gets 0 but still refreshes the marker.
var claimPlayScript = redis.NewScript(`
local key   = KEYS[1]
local track = ARGV[1]
local start = tonumber(ARGV[2])
local gap   = tonumber(ARGV[3])
local ttl   = tonumber(ARGV[4])

-- Format the start explicitly: Redis runs Lua 5.1, where concatenating a number
-- goes through %.14g and would eventually reach for scientific notation, which
-- does not survive the tonumber round-trip below intact.
local marker = track .. '|' .. string.format('%d', start)

local claimed = 1
local prev = redis.call('GET', key)
if prev then
  local sep = string.find(prev, '|', 1, true)
  if sep then
    local ptrack = string.sub(prev, 1, sep - 1)
    local pstart = tonumber(string.sub(prev, sep + 1))
    if ptrack == track then
      if start == 0 or pstart == nil or pstart == 0 then
        claimed = 0
      else
        local delta = start - pstart
        if delta < 0 then delta = -delta end
        if delta <= gap then claimed = 0 end
      end
    end
  end
end

redis.call('SET', key, marker, 'EX', ttl)
return claimed
`)

// claimPlay reports whether this process should record the given play.
//
// The markers on Presence are per-process, so on their own they let every node
// in a multi-node deployment record the same play (each runs its own gateway
// connection, and external reports fan out over zruvix:global_sync), and let a
// restart re-record whatever the user is listening to at that moment. This
// second guard lives in Redis, so the play is counted once no matter how many
// nodes saw it or how often the service restarts.
func claimPlay(uid, track string, start int64) bool {
	if track == "" {
		return false
	}
	return claimPlayScript.RunInt(
		[]string{lastPlayKey(uid)},
		track, start, sameTrackReplayMinGapMs, lastPlayTTL,
	) == 1
}

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
	newStatus := pretty.DiscordStatus
	newTrack := trackKey(pretty.NowPlaying)
	var newStart int64
	if newTrack != "" {
		newStart = trackStart(pretty.NowPlaying)
	}

	p.mu.Lock()
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

	// The in-memory check above is the cheap first pass — it rejects the bulk of
	// the per-guild re-reports without touching Redis. Only once it thinks this
	// is a new play do we pay for claimPlay, which settles it across nodes and
	// across restarts. Everything below runs exactly once per real play.
	if !trackChanged || pretty.NowPlaying == nil || !claimPlay(p.UserID, newTrack, newStart) {
		return
	}

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
	redis.HIncrBy(playCountsKey(p.UserID), newTrack, 1)
	meta := nowPlayingToMap(pretty.NowPlaying)
	meta["last_played"] = now
	if mb, err := json.Marshal(meta); err == nil {
		redis.HSet(trackMetaKey(p.UserID), newTrack, string(mb))
	}

	k := tracksTodayKey(p.UserID)
	redis.Incr(k)
	redis.Expire(k, 60*60*48) // keep daily counters ~2 days
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
	redis.Del(lastPlayKey(uid))

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
		}
		// track_key is what the entry is counted under and is always present;
		// track_id stays whatever the source actually gave us, so it is null for
		// tracks we identified by song/artist rather than by a real id.
		entry["track_key"] = t.id
		if _, ok := entry["track_id"]; !ok {
			if strings.HasPrefix(t.id, syntheticTrackKeyPrefix) {
				entry["track_id"] = nil
			} else {
				entry["track_id"] = t.id
			}
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
