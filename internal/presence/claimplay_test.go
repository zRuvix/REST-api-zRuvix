package presence

import (
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"zruvix/internal/redis"
)

// startRedis points the redis package at an in-process server for the duration
// of a test. claimPlay's guard runs as a Lua script server-side and fails closed
// on any error, so it can only be trusted if it is exercised against a real
// Redis command surface rather than mocked out.
func startRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	s := miniredis.RunT(t)
	if err := redis.Connect("redis://" + s.Addr()); err != nil {
		t.Fatalf("connect miniredis: %v", err)
	}
	return s
}

// TestClaimPlay covers the shared new-play guard. It mirrors isNewPlay, but in
// Redis, so that a play is counted once no matter how many nodes observed it.
func TestClaimPlay(t *testing.T) {
	const base int64 = 1_700_000_000_000

	t.Run("first play is claimed", func(t *testing.T) {
		startRedis(t)
		if !claimPlay("u1", "trackA", base) {
			t.Error("the first play of a track must be claimed")
		}
	})

	t.Run("untrackable song is never claimed", func(t *testing.T) {
		startRedis(t)
		if claimPlay("u1", "", base) {
			t.Error("an empty track key must not be claimed")
		}
	})

	t.Run("jittered re-report is rejected", func(t *testing.T) {
		startRedis(t)
		if !claimPlay("u1", "trackA", base) {
			t.Fatal("setup: first play should be claimed")
		}
		for _, jitter := range []int64{0, 250, -400, 1200, sameTrackReplayMinGapMs} {
			if claimPlay("u1", "trackA", base+jitter) {
				t.Errorf("jitter %+d was claimed as a new play", jitter)
			}
		}
	})

	t.Run("genuine replay is claimed", func(t *testing.T) {
		startRedis(t)
		claimPlay("u1", "trackA", base)
		if !claimPlay("u1", "trackA", base+180_000) {
			t.Error("a restart of the same track must count as a new play")
		}
	})

	t.Run("different track is claimed", func(t *testing.T) {
		startRedis(t)
		claimPlay("u1", "trackA", base)
		if !claimPlay("u1", "trackB", base+2000) {
			t.Error("a different track must count as a new play")
		}
	})

	t.Run("unknown start is not a replay", func(t *testing.T) {
		startRedis(t)
		if !claimPlay("u1", "trackA", 0) {
			t.Fatal("setup: first play should be claimed")
		}
		if claimPlay("u1", "trackA", 0) {
			t.Error("same track with no start info must not re-count")
		}
	})

	t.Run("users are independent", func(t *testing.T) {
		startRedis(t)
		claimPlay("u1", "trackA", base)
		if !claimPlay("u2", "trackA", base) {
			t.Error("one user's play must not block another's")
		}
	})

	t.Run("track cycles back and forth", func(t *testing.T) {
		startRedis(t)
		// A -> B -> A is three distinct plays; only the last one is at risk of
		// being swallowed, since the marker holds just the previous play.
		claims := 0
		for _, p := range []struct {
			track string
			start int64
		}{{"A", base}, {"B", base + 200_000}, {"A", base + 400_000}} {
			if claimPlay("u1", p.track, p.start) {
				claims++
			}
		}
		if claims != 3 {
			t.Errorf("A->B->A produced %d plays, want 3", claims)
		}
	})
}

// TestClaimPlayMultiNode is the regression test for the counts-per-node bug:
// several processes each see the same play (each runs its own gateway
// connection, and external reports fan out over global_sync) with slightly
// different start timestamps, and between them must record exactly one play.
func TestClaimPlayMultiNode(t *testing.T) {
	startRedis(t)

	const base int64 = 1_700_000_000_000
	starts := []int64{base, base + 250, base - 400, base + 1200, base + 80, base - 50}

	var mu sync.Mutex
	var wg sync.WaitGroup
	claims := 0
	for _, s := range starts {
		wg.Add(1)
		go func(start int64) {
			defer wg.Done()
			if claimPlay("u1", "trackA", start) {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}(s)
	}
	wg.Wait()

	if claims != 1 {
		t.Fatalf("%d nodes recorded %d plays for one song, want 1", len(starts), claims)
	}
}

// TestClaimPlaySurvivesRestart is the regression test for counts inflating on
// deploy: the in-memory markers are gone after a restart, so the first presence
// update re-offers whatever the user is currently listening to.
func TestClaimPlaySurvivesRestart(t *testing.T) {
	s := startRedis(t)

	const base int64 = 1_700_000_000_000
	if !claimPlay("u1", "trackA", base) {
		t.Fatal("setup: first play should be claimed")
	}

	// Restart: same Redis, fresh process (empty lastTrackID/lastTrackStart), and
	// Discord re-reports the in-progress song with a slightly moved start.
	if err := redis.Connect("redis://" + s.Addr()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if claimPlay("u1", "trackA", base+900) {
		t.Error("the song playing across a restart was counted twice")
	}
}
