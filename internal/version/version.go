// Package version is the single source of truth for the zRuvix version and its
// changelog. Bump Version and prepend a Release here when shipping changes;
// the value flows into the bot (?stats), the docs page, and /v1/version.
package version

// Version is the current zRuvix release. Use semantic versioning.
const Version = "1.7.1"

// Release is one entry in the changelog.
type Release struct {
	Version string   `json:"version"`
	Date    string   `json:"date"` // YYYY-MM-DD
	Title   string   `json:"title"`
	Changes []string `json:"changes"`
}

// Changelog lists releases newest-first.
var Changelog = []Release{
	{
		Version: "1.7.1",
		Date:    "2026-07-26",
		Title:   "Top Tracks Accuracy Fix",
		Changes: []string{
			"Fixed songs with no track id never being counted at all — YouTube Music activities Discord sends without a track link, and external reports carrying only a song and artist, were missing from top-tracks, listening history and the daily counter entirely; they are now grouped by song + artist under a derived `track_key`",
			"Fixed play counts being multiplied across nodes: the duplicate guard lived in each process's memory, so every node recorded the same play. It now settles atomically in Redis",
			"Fixed a restart or deploy re-counting whatever each user was listening to at that moment",
			"Fixed a paused or position-less external report booking a new play on every refresh — its start no longer drifts forward each time",
			"top-tracks entries now always carry `track_key` (what the play was counted under); `track_id` stays null when the source never gave one",
		},
	},
	{
		Version: "1.7.0",
		Date:    "2026-07-25",
		Title:   "External Now Playing (Pear Desktop)",
		Changes: []string{
			"New PUT/DELETE /v1/users/@me/now-playing and /v1/users/:id/now-playing for non-Discord music reporters (API key required)",
			"External reports fill youtube_music / now_playing when Discord has no Spotify session — works while Discord is offline",
			"Reports expire after 5 minutes without a refresh; history and top-tracks still record track changes",
			"Multi-node sync via zruvix:global_sync for external_music diffs",
		},
	},
	{
		Version: "1.6.1",
		Date:    "2026-06-27",
		Title:   "Listening History Duplicate Fix",
		Changes: []string{
			"Fixed the same song being recorded multiple times in listening history (e.g. duplicate \"0m ago\" entries) when a user shared several mutual servers with the bot",
			"Fixed play counts and the daily track counter being inflated by those duplicates — top-tracks now reflects real play counts",
			"The same track is now only counted again on a genuine replay, ignoring the small start-time jitter Discord sends across per-server presence updates",
		},
	},
	{
		Version: "1.6.0",
		Date:    "2026-06-26",
		Title:   "Health Endpoint & Shareable Profiles",
		Changes: []string{
			"New `GET /v1/health` endpoint returning service status, Redis connectivity, gateway state, uptime, and monitored user count",
			"Shareable profile pages at `docs.zruvix.com/u/:id` showing live avatar, status, banner, custom status, and now playing",
		},
	},
	{
		Version: "1.5.1",
		Date:    "2026-06-26",
		Title:   "Top Tracks Fix, Dynamic Changelog & Banner Quicklink",
		Changes: []string{
			"Fixed repeated plays of the same song not incrementing play count (now detects track restart via timestamps)",
			"Fixed top tracks list only showing tracks with 2+ plays — now includes all played tracks",
			"New dynamic changelog API: `GET /v1/changelog` (public) and `POST /v1/changelog` (auth required) — docs page updates live without redeploying",
			"New banner quicklink proxy at `/banner/{id}.{ext}` — works like the avatar quicklink but for user banners",
		},
	},
	{
		Version: "1.5.0",
		Date:    "2026-06-25",
		Title:   "Listening History+, Top Tracks & Data Deletion",
		Changes: []string{
			"Track history now stores the full now_playing object (album_art_url, track_url, timestamps, duration_ms) so history can render album art and progress",
			"New most-played endpoint /v1/users/:id/top-tracks?limit=10 ranking tracks by play count, each with full song details",
			"New ?forget command lets users delete their own presence, listening history and most-played data",
		},
	},
	{
		Version: "1.4.0",
		Date:    "2026-06-25",
		Title:   "Banner & Member Since",
		Changes: []string{
			"New banner, banner_url and accent_color fields (lazily fetched from Discord and cached)",
			"New member_since field: the Discord account creation time, derived from the user-id snowflake",
		},
	},
	{
		Version: "1.3.0",
		Date:    "2026-06-25",
		Title:   "Custom Status",
		Changes: []string{
			"New custom_status field exposing the user's Discord custom status text and emoji",
			"Docs now live at a dedicated site; /v1/docs redirects to DOCS_URL (docs.zruvix.com)",
		},
	},
	{
		Version: "1.2.0",
		Date:    "2026-06-25",
		Title:   "Now Playing, Status Cards, History & Docs",
		Changes: []string{
			"Unified now_playing object normalizing Spotify and YouTube Music",
			"Live animated SVG status card at /v1/users/:id/card.svg",
			"Presence history (/v1/users/:id/history) and stats (/v1/users/:id/stats)",
			"Documentation page at /v1/docs with this changelog",
			"New /v1/version endpoint and centralized version system",
			"Faster-failing Redis timeouts so handlers never hang when Redis is down",
		},
	},
	{
		Version: "1.1.0",
		Date:    "2026-06-25",
		Title:   "Bot UX & YouTube Music",
		Changes: []string{
			"Embed-based bot responses with plain-language wording",
			"New commands: help, me, stats, ping, count, clear, invite, list",
			"Default command prefix changed to '?'",
			"YouTube Music detection (listening_to_youtube_music / youtube_music)",
		},
	},
	{
		Version: "1.0.0",
		Date:    "2026-06-25",
		Title:   "Initial release",
		Changes: []string{
			"Go port of the Lanyard API (REST + WebSocket) as zRuvix",
			"Discord gateway client, presence registry, and KV store",
			"Discord bot with KV commands; Redis storage with cross-node sync",
			"Prometheus metrics, .env configuration, and setup.sh installer/service",
		},
	},
}
