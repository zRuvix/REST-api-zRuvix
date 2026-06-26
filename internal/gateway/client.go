// Package gateway implements the Discord gateway WebSocket client: it connects,
// identifies (or resumes), heartbeats, and translates Discord dispatch events
// into presence registry updates. It mirrors zRuvix.Gateway.Client.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"zruvix/internal/config"
	"zruvix/internal/metrics"
	"zruvix/internal/presence"
)

// Discord gateway opcodes.
const (
	opDispatch            = 0
	opHeartbeat           = 1
	opIdentify            = 2
	opStatusUpdate        = 3
	opResume              = 6
	opReconnect           = 7
	opRequestGuildMembers = 8
	opInvalidSession      = 9
	opHello               = 10
	opHeartbeatACK        = 11
)

// Gateway intents (only those zRuvix needs).
const (
	intentGuilds         = 1 << 0
	intentGuildMembers   = 1 << 1
	intentGuildPresences = 1 << 8
	intentGuildMessages  = 1 << 9
	intentDirectMessages = 1 << 12
	intentMessageContent = 1 << 15

	intentsMask = intentGuilds | intentGuildMembers | intentGuildPresences |
		intentGuildMessages | intentDirectMessages | intentMessageContent
)

// OnMessageCreate, if set, is invoked for MESSAGE_CREATE dispatch events. It is
// wired to bot.HandleMessage at startup, keeping this package free of a bot
// import (and thus free of an import cycle).
var OnMessageCreate func(data map[string]any)

var errInvalidSession = errors.New("invalid session")

// connected tracks whether the gateway is currently connected.
var connected atomic.Bool

// Connected returns true if the gateway WebSocket is connected.
func Connected() bool { return connected.Load() }

// session carries resume state across reconnects.
type session struct {
	token     string
	sessionID string
	resumeURL string
	seq       atomic.Int64
	hasSeq    atomic.Bool
}

func (s *session) setSeq(v int64) {
	s.seq.Store(v)
	s.hasSeq.Store(true)
}

// seqValue returns the current sequence number, or nil when none has been seen.
func (s *session) seqValue() any {
	if s.hasSeq.Load() {
		return s.seq.Load()
	}
	return nil
}

// gwConn serialises writes to the websocket (coder/websocket disallows
// concurrent writers).
type gwConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func (c *gwConn) send(ctx context.Context, op int, d any) error {
	b, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Run connects to Discord and keeps the connection alive, resuming or
// re-identifying as needed. It blocks forever.
func Run(token string) {
	s := &session{token: token}
	for {
		err := connectAndServe(s)
		switch {
		case errors.Is(err, errInvalidSession):
			// Clear resume data and start a fresh session.
			s.sessionID = ""
			s.resumeURL = ""
			s.hasSeq.Store(false)
		}
		time.Sleep(time.Second)
	}
}

func gatewayURL(s *session) string {
	if s.resumeURL != "" {
		return s.resumeURL + "?v=10&encoding=json"
	}
	return "wss://gateway.discord.gg/?v=10&encoding=json"
}

func connectAndServe(s *session) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws, _, err := websocket.Dial(ctx, gatewayURL(s), nil)
	if err != nil {
		log.Printf("Discord: dial failed: %v", err)
		return err
	}
	ws.SetReadLimit(1 << 24) // 16MB — GUILD_CREATE with all presences is large
	defer ws.CloseNow()

	gw := &gwConn{ws: ws}
	ackCh := make(chan struct{}, 1)
	stopHB := make(chan struct{})
	hbStarted := false
	connected.Store(true)
	defer func() {
		connected.Store(false)
		if hbStarted {
			close(stopHB)
		}
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			log.Printf("Discord: websocket disconnected: %v, will attempt resume", err)
			return nil
		}

		var msg struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
			S  *int64          `json:"s"`
			T  string          `json:"t"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.S != nil {
			s.setSeq(*msg.S)
		}

		switch msg.Op {
		case opHello:
			var hd struct {
				HeartbeatInterval float64 `json:"heartbeat_interval"`
			}
			json.Unmarshal(msg.D, &hd)
			go heartbeat(ctx, gw, s, hd.HeartbeatInterval, ackCh, stopHB)
			hbStarted = true

			if s.sessionID != "" && s.resumeURL != "" {
				log.Printf("Discord: Resuming session %s", s.sessionID)
				gw.send(ctx, opResume, map[string]any{
					"token":      s.token,
					"session_id": s.sessionID,
					"seq":        s.seqValue(),
				})
			} else {
				gw.send(ctx, opIdentify, identifyPayload(s))
			}

		case opHeartbeatACK:
			select {
			case ackCh <- struct{}{}:
			default:
			}

		case opReconnect:
			log.Println("Discord enforced Reconnect, will resume session")
			return nil

		case opInvalidSession:
			log.Println("Discord: Invalid session, starting new session")
			return errInvalidSession

		case opDispatch:
			handleDispatch(ctx, gw, s, msg.T, msg.D)
		}
	}
}

func identifyPayload(s *session) map[string]any {
	return map[string]any{
		"token": s.token,
		"properties": map[string]any{
			"$os":               "go",
			"$browser":          "zRuvix-worker",
			"$device":           "zRuvix-go",
			"$referrer":         "",
			"$referring_domain": "",
		},
		"presence": map[string]any{
			"since": nil,
			"game": map[string]any{
				"name": config.C.BotPresence,
				"type": config.C.BotPresenceType,
			},
			"status": "online",
		},
		"compress":        false,
		"large_threshold": 250,
		"intents":         intentsMask,
	}
}

// heartbeat sends Opcode 1 on the interval and closes the socket (to trigger a
// resume) if Discord stops acknowledging.
func heartbeat(ctx context.Context, gw *gwConn, s *session, intervalMs float64, ackCh <-chan struct{}, stop <-chan struct{}) {
	interval := time.Duration(intervalMs) * time.Millisecond
	ack := true
	timer := time.NewTimer(0) // beat immediately, as the Elixir Heartbeat does
	defer timer.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ackCh:
			ack = true
		case <-timer.C:
			if !ack {
				log.Println("Discord: Heartbeat stale, will resume session")
				gw.ws.Close(websocket.StatusGoingAway, "heartbeat stale")
				return
			}
			ack = false
			gw.send(ctx, opHeartbeat, s.seqValue())
			timer.Reset(interval)
		}
	}
}

func handleDispatch(ctx context.Context, gw *gwConn, s *session, event string, raw json.RawMessage) {
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return
	}

	switch event {
	case "READY":
		s.sessionID = getString(d, "session_id")
		s.resumeURL = getString(d, "resume_gateway_url")
		log.Println("Discord: Ready")

	case "GUILD_CREATE":
		createMemberPresences(d)
		// The zRuvix guild exceeds large_threshold, so request all members.
		gw.send(ctx, opRequestGuildMembers, map[string]any{
			"guild_id":  getString(d, "id"),
			"limit":     0,
			"query":     "",
			"presences": true,
		})

	case "GUILD_MEMBERS_CHUNK":
		createMemberPresences(d)

	case "PRESENCE_UPDATE":
		metrics.PresenceUpdates.Inc()
		if uid := userID(d); uid != "" {
			presence.Reg.SyncLocal(uid, map[string]any{"discord_presence": d})
		}

	case "GUILD_MEMBER_ADD":
		gw.send(ctx, opRequestGuildMembers, map[string]any{
			"guild_id":  getString(d, "guild_id"),
			"user_ids":  []any{userID(d)},
			"limit":     1,
			"presences": true,
		})

	case "GUILD_MEMBER_UPDATE":
		if user, ok := d["user"].(map[string]any); ok {
			if uid, _ := user["id"].(string); uid != "" {
				presence.Reg.SyncLocal(uid, map[string]any{"discord_user": user})
			}
		}

	case "GUILD_MEMBER_REMOVE":
		if uid := userID(d); uid != "" {
			presence.Reg.Stop(uid)
		}

	case "MESSAGE_CREATE":
		if config.C.IsIdempotent && OnMessageCreate != nil {
			go OnMessageCreate(d)
		}
	}
}

// createMemberPresences starts/updates a presence for each member in a
// GUILD_CREATE or GUILD_MEMBERS_CHUNK payload, matching it with its presence.
func createMemberPresences(d map[string]any) {
	members, _ := d["members"].([]any)
	presences, _ := d["presences"].([]any)

	for _, m := range members {
		member, ok := m.(map[string]any)
		if !ok {
			continue
		}
		user, ok := member["user"].(map[string]any)
		if !ok {
			continue
		}
		uid, _ := user["id"].(string)
		if uid == "" {
			continue
		}

		var pres map[string]any
		for _, p := range presences {
			if pm, ok := p.(map[string]any); ok {
				if pu, ok := pm["user"].(map[string]any); ok {
					if pid, _ := pu["id"].(string); pid == uid {
						pres = pm
						break
					}
				}
			}
		}

		presence.Reg.LookupOrStart(uid, pres, user)
		presence.Reg.SyncLocal(uid, map[string]any{
			"user_id":          uid,
			"discord_presence": pres,
			"discord_user":     user,
		})
	}
}

// userID extracts d["user"]["id"].
func userID(d map[string]any) string {
	if user, ok := d["user"].(map[string]any); ok {
		if id, ok := user["id"].(string); ok {
			return id
		}
	}
	return ""
}

func getString(d map[string]any, key string) string {
	if v, ok := d[key].(string); ok {
		return v
	}
	return ""
}
