package presence

import (
	"encoding/json"
	"strings"
	"time"

	"zruvix/internal/redis"
)

// ExternalMusicTTL is how long an external (non-Discord) now-playing report
// remains live without a refresh. Pear Desktop and other clients should
// re-POST within this window while a track is active.
const ExternalMusicTTL = 5 * time.Minute

// ExternalMusic is a client-reported now-playing payload (e.g. Pear Desktop).
// It is merged into PrettyPresence when Discord has no Spotify activity.
type ExternalMusic struct {
	Source      string `json:"source"` // "youtube_music" (default)
	Song        string `json:"song"`
	Artist      string `json:"artist"`
	Album       string `json:"album,omitempty"`
	AlbumArtURL string `json:"album_art_url,omitempty"`
	TrackID     string `json:"track_id,omitempty"`
	TrackURL    string `json:"track_url,omitempty"`
	// Timestamps is {start, end} in Unix ms when known.
	Timestamps map[string]int64 `json:"timestamps,omitempty"`
	DurationMs *int64           `json:"duration_ms,omitempty"`
	IsPaused   bool             `json:"is_paused"`
	// UpdatedAt is when the report was accepted (Unix ms). Used for TTL.
	UpdatedAt int64 `json:"updated_at"`
}

// ExternalMusicInput is the write body for PUT .../now-playing.
type ExternalMusicInput struct {
	Source      string `json:"source"`
	Song        string `json:"song"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtURL string `json:"album_art_url"`
	TrackID     string `json:"track_id"`
	TrackURL    string `json:"track_url"`
	// ProgressMs is playback position; used with DurationMs to build timestamps.
	ProgressMs *int64 `json:"progress_ms"`
	DurationMs *int64 `json:"duration_ms"`
	// Timestamps may be provided directly (Unix ms). Overrides progress-based calc.
	Timestamps map[string]int64 `json:"timestamps"`
	IsPaused   bool             `json:"is_paused"`
}

func externalMusicKey(uid string) string { return "zruvix_external_music:" + uid }

// Live returns true if the report is within ExternalMusicTTL.
func (em *ExternalMusic) Live() bool {
	if em == nil || (em.Song == "" && em.Artist == "") {
		return false
	}
	if em.UpdatedAt <= 0 {
		return false
	}
	return time.Since(time.UnixMilli(em.UpdatedAt)) < ExternalMusicTTL
}

// SetExternalMusic validates and stores an external now-playing report for a
// monitored user, then rebuilds presence and notifies subscribers.
func SetExternalMusic(userID string, in ExternalMusicInput) (*PrettyPresence, *Error) {
	p, err := GetPresence(userID)
	if err != nil {
		return nil, err
	}

	song := strings.TrimSpace(in.Song)
	artist := strings.TrimSpace(in.Artist)
	if song == "" && artist == "" {
		return nil, &Error{
			HTTPCode: 400,
			Code:     "invalid_now_playing",
			Message:  "song or artist is required",
		}
	}

	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "youtube_music"
	}

	now := time.Now().UnixMilli()
	em := &ExternalMusic{
		Source:      source,
		Song:        song,
		Artist:      artist,
		Album:       strings.TrimSpace(in.Album),
		AlbumArtURL: strings.TrimSpace(in.AlbumArtURL),
		TrackID:     strings.TrimSpace(in.TrackID),
		TrackURL:    strings.TrimSpace(in.TrackURL),
		IsPaused:    in.IsPaused,
		UpdatedAt:   now,
	}

	if in.DurationMs != nil && *in.DurationMs > 0 {
		d := *in.DurationMs
		em.DurationMs = &d
	}

	// Prefer explicit timestamps; otherwise derive from progress + duration.
	if in.Timestamps != nil {
		start, sok := in.Timestamps["start"]
		end, eok := in.Timestamps["end"]
		if sok && eok && end > start {
			em.Timestamps = map[string]int64{"start": start, "end": end}
			if em.DurationMs == nil {
				d := end - start
				em.DurationMs = &d
			}
		}
	} else if em.DurationMs != nil && *em.DurationMs > 0 {
		progress := int64(0)
		if in.ProgressMs != nil && *in.ProgressMs > 0 {
			progress = *in.ProgressMs
			if progress > *em.DurationMs {
				progress = *em.DurationMs
			}
		}
		start := now - progress
		end := start + *em.DurationMs
		em.Timestamps = map[string]int64{"start": start, "end": end}
	}

	// Infer track_id from YouTube URLs when missing.
	if em.TrackID == "" && em.TrackURL != "" {
		if id := youtubeTrackID(&em.TrackURL); id != nil {
			em.TrackID = *id
		}
	}

	if b, err := json.Marshal(em); err == nil {
		redis.SetEX(externalMusicKey(userID), string(b), ExternalMusicTTL)
	}

	// Fan-out via Sync so multi-node deployments pick this up.
	Reg.Sync(userID, map[string]any{"external_music": em}, false)

	pretty := p.BuildPretty()
	return &pretty, nil
}

// ClearExternalMusic removes any external now-playing report for the user.
func ClearExternalMusic(userID string) (*PrettyPresence, *Error) {
	p, err := GetPresence(userID)
	if err != nil {
		return nil, err
	}

	redis.Del(externalMusicKey(userID))
	Reg.Sync(userID, map[string]any{"external_music": nil}, false)

	pretty := p.BuildPretty()
	return &pretty, nil
}

// loadExternalMusic reads a stored report from Redis (used on presence start).
func loadExternalMusic(userID string) *ExternalMusic {
	raw := redis.Get(externalMusicKey(userID))
	if raw == "" {
		return nil
	}
	var em ExternalMusic
	if err := json.Unmarshal([]byte(raw), &em); err != nil {
		return nil
	}
	if !em.Live() {
		return nil
	}
	return &em
}

// parseExternalMusic converts a sync-diff value into *ExternalMusic.
// A JSON null / explicit nil clears the field.
func parseExternalMusic(v any) *ExternalMusic {
	if v == nil {
		return nil
	}
	switch m := v.(type) {
	case *ExternalMusic:
		if m != nil && m.Live() {
			return m
		}
		return nil
	case ExternalMusic:
		if m.Live() {
			cp := m
			return &cp
		}
		return nil
	case map[string]any:
		b, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		var em ExternalMusic
		if err := json.Unmarshal(b, &em); err != nil {
			return nil
		}
		if !em.Live() {
			return nil
		}
		return &em
	default:
		return nil
	}
}

// externalToYouTubeMusic maps an external report into the public youtube_music shape.
func externalToYouTubeMusic(em *ExternalMusic) *YouTubeMusic {
	if em == nil || !em.Live() {
		return nil
	}
	yt := &YouTubeMusic{
		Artist: em.Artist,
		Song:   em.Song,
	}
	if em.Album != "" {
		a := em.Album
		yt.Album = &a
	}
	if em.AlbumArtURL != "" {
		u := em.AlbumArtURL
		yt.AlbumArtURL = &u
	}
	if em.TrackID != "" {
		id := em.TrackID
		yt.TrackID = &id
	}
	if em.TrackURL != "" {
		u := em.TrackURL
		yt.URL = &u
	}
	if len(em.Timestamps) >= 2 {
		yt.Timestamps = map[string]any{
			"start": em.Timestamps["start"],
			"end":   em.Timestamps["end"],
		}
	}
	return yt
}

// mergeExternalMusic applies external now-playing when Discord Spotify is absent.
// Priority: Discord Spotify > live external > Discord YouTube Music.
func mergeExternalMusic(pretty *PrettyPresence, em *ExternalMusic) {
	if pretty == nil || em == nil || !em.Live() {
		return
	}
	// Never override a live Discord Spotify session.
	if pretty.ListeningToSpotify && pretty.Spotify != nil {
		return
	}

	yt := externalToYouTubeMusic(em)
	if yt == nil {
		return
	}

	pretty.ListeningToYouTubeMusic = true
	pretty.YouTubeMusic = yt
	pretty.NowPlaying = buildNowPlaying(pretty.Spotify, yt)
	// Annotate source when the client sent something other than youtube_music.
	if pretty.NowPlaying != nil && em.Source != "" && em.Source != "youtube_music" {
		pretty.NowPlaying.Source = em.Source
	}
}
