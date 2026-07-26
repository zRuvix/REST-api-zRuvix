package presence

import (
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

// TestTrackKey covers the identity a play is counted under. The regression it
// guards against: sources that give us no track id (a YouTube Music activity
// with no details_url, an external reporter sending only song + artist) used to
// produce an empty key, which dropped the play from top-tracks, listening
// history and the daily counter entirely.
func TestTrackKey(t *testing.T) {
	t.Run("real track id wins", func(t *testing.T) {
		np := &NowPlaying{Source: "spotify", TrackID: strPtr("4cOdK2wGLETKBW3PvgPWqT"),
			Song: "Reminder", Artist: "The Weeknd"}
		if got := trackKey(np); got != "4cOdK2wGLETKBW3PvgPWqT" {
			t.Errorf("trackKey = %q, want the raw track id", got)
		}
	})

	t.Run("nil now playing", func(t *testing.T) {
		if got := trackKey(nil); got != "" {
			t.Errorf("trackKey(nil) = %q, want empty", got)
		}
	})

	t.Run("missing track id falls back to a synthetic key", func(t *testing.T) {
		np := &NowPlaying{Source: "youtube_music", Song: "Reminder", Artist: "The Weeknd"}
		got := trackKey(np)
		if got == "" {
			t.Fatal("trackKey = empty; the play would be dropped")
		}
		if !strings.HasPrefix(got, syntheticTrackKeyPrefix) {
			t.Errorf("trackKey = %q, want the %q prefix", got, syntheticTrackKeyPrefix)
		}
	})

	t.Run("empty track id string falls back too", func(t *testing.T) {
		np := &NowPlaying{Source: "youtube_music", TrackID: strPtr(""), Song: "Reminder", Artist: "The Weeknd"}
		if !strings.HasPrefix(trackKey(np), syntheticTrackKeyPrefix) {
			t.Error("empty track_id should fall back to a synthetic key")
		}
	})

	t.Run("synthetic key is stable across reports", func(t *testing.T) {
		a := trackKey(&NowPlaying{Source: "youtube_music", Song: "Reminder", Artist: "The Weeknd"})
		b := trackKey(&NowPlaying{Source: "youtube_music", Song: "  REMINDER ", Artist: "the weeknd"})
		if a != b {
			t.Errorf("same song produced two keys: %q vs %q", a, b)
		}
	})

	t.Run("different songs get different keys", func(t *testing.T) {
		a := trackKey(&NowPlaying{Source: "youtube_music", Song: "Reminder", Artist: "The Weeknd"})
		b := trackKey(&NowPlaying{Source: "youtube_music", Song: "Starboy", Artist: "The Weeknd"})
		if a == b {
			t.Errorf("distinct songs collided on key %q", a)
		}
	})

	t.Run("no song and no artist is not countable", func(t *testing.T) {
		if got := trackKey(&NowPlaying{Source: "youtube_music"}); got != "" {
			t.Errorf("trackKey = %q, want empty for a track we cannot identify", got)
		}
	})
}

// TestSameExternalTrack covers the check that decides whether an incoming
// external report is a refresh of the track we already hold (and may therefore
// reuse its timestamps instead of recomputing a start that drifts forward).
func TestSameExternalTrack(t *testing.T) {
	cases := []struct {
		name string
		a, b *ExternalMusic
		want bool
	}{
		{"nil previous", nil, &ExternalMusic{Song: "Reminder"}, false},
		{"matching track ids", &ExternalMusic{TrackID: "a40tAP5MC6M"}, &ExternalMusic{TrackID: "a40tAP5MC6M"}, true},
		{"differing track ids", &ExternalMusic{TrackID: "a40tAP5MC6M"}, &ExternalMusic{TrackID: "xyz"}, false},
		{
			"no ids, same song and artist",
			&ExternalMusic{Song: "Reminder", Artist: "The Weeknd"},
			&ExternalMusic{Song: "reminder", Artist: "the weeknd "},
			true,
		},
		{
			"no ids, different song",
			&ExternalMusic{Song: "Reminder", Artist: "The Weeknd"},
			&ExternalMusic{Song: "Starboy", Artist: "The Weeknd"},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameExternalTrack(tc.a, tc.b); got != tc.want {
				t.Errorf("sameExternalTrack = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsNewPlay covers the track-change/replay detection that drives history
// recording and play counting. The key regression it guards against: the same
// song re-reported with a slightly jittered start (Discord sends one
// PRESENCE_UPDATE per mutual guild) must NOT be counted as a new play.
func TestIsNewPlay(t *testing.T) {
	const base int64 = 1_700_000_000_000

	cases := []struct {
		name                       string
		newTrack, lastTrackID      string
		newStart, lastTrackStart   int64
		want                       bool
	}{
		{
			name:      "nothing playing",
			newTrack:  "", lastTrackID: "abc",
			newStart:  0, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "first ever track",
			newTrack:  "abc", lastTrackID: "",
			newStart:  base, lastTrackStart: 0,
			want:      true,
		},
		{
			name:      "different song",
			newTrack:  "def", lastTrackID: "abc",
			newStart:  base + 5000, lastTrackStart: base,
			want:      true,
		},
		{
			name:      "same song, no start jitter",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "same song, small forward jitter (per-guild re-report)",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base + 1500, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "same song, small backward jitter",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base - 1500, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "same song, jitter just under threshold",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base + sameTrackReplayMinGapMs, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "same song, genuine replay (start jumps well past threshold)",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base + 180_000, lastTrackStart: base,
			want:      true,
		},
		{
			name:      "same song, new start unknown -> not a replay",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  0, lastTrackStart: base,
			want:      false,
		},
		{
			name:      "same song, previous start unknown -> not a replay",
			newTrack:  "abc", lastTrackID: "abc",
			newStart:  base, lastTrackStart: 0,
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isNewPlay(tc.newTrack, tc.lastTrackID, tc.newStart, tc.lastTrackStart)
			if got != tc.want {
				t.Errorf("isNewPlay(%q, %q, %d, %d) = %v, want %v",
					tc.newTrack, tc.lastTrackID, tc.newStart, tc.lastTrackStart, got, tc.want)
			}
		})
	}
}

// TestIsNewPlaySingleSongStream simulates a single continuous play that arrives
// as many jittered PRESENCE_UPDATE events (one per mutual guild, plus position
// refreshes). Exactly one of them should register as a new play.
func TestIsNewPlaySingleSongStream(t *testing.T) {
	const track = "track123"
	const base int64 = 1_700_000_000_000

	// First event has no prior state; subsequent ones carry small jitter.
	starts := []int64{base, base + 250, base - 400, base + 1200, base + 80, base - 50}

	lastTrackID := ""
	var lastTrackStart int64
	plays := 0
	for _, s := range starts {
		if isNewPlay(track, lastTrackID, s, lastTrackStart) {
			plays++
		}
		lastTrackID = track
		lastTrackStart = s
	}

	if plays != 1 {
		t.Fatalf("continuous single-song stream registered %d plays, want 1", plays)
	}
}
