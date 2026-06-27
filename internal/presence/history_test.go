package presence

import "testing"

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
