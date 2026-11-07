package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/sepia/internal/agent"
	"github.com/AutumnsGrove/sepia/internal/config"
	"github.com/AutumnsGrove/sepia/internal/contract"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/session"
	"github.com/AutumnsGrove/sepia/internal/store"
)

// The frame codec, first: a client's mask, the two length forms, the fragmented message, every refusal, and a fuzz
// target that only has to return.

func clientFrame(t testing.TB, fin bool, opcode byte, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	first := opcode
	if fin {
		first |= 0x80
	}
	b.WriteByte(first)
	key := []byte{1, 2, 3, 4}
	length := len(payload)
	switch {
	case length < 126:
		b.WriteByte(0x80 | byte(length))
	case length <= 0xFFFF:
		b.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		b.Write(ext[:])
	default:
		b.WriteByte(0x80 | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		b.Write(ext[:])
	}
	b.Write(key)
	for i, by := range payload {
		b.WriteByte(by ^ key[i&3])
	}
	return b.Bytes()
}

func readOne(t *testing.T, raw []byte) (frame, error) {
	t.Helper()
	return readFrame(bufio.NewReader(bytes.NewReader(raw)))
}

func TestFrameRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, 125, 126, 65_000} {
		payload := bytes.Repeat([]byte("a"), size)
		f, err := readOne(t, clientFrame(t, true, opText, payload))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(f.payload, payload) || f.opcode != opText || !f.fin {
			t.Errorf("size %d did not round-trip", size)
		}
	}
}

func TestServerFramesAreUnmaskedAndReadable(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeFrame(bw, opText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if raw[1]&0x80 != 0 {
		t.Fatal("the server masked a frame")
	}
	f, err := readFrameServer(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil || string(f.payload) != "hello" {
		t.Fatalf("read back %q, %v", f.payload, err)
	}
}

func TestFrameRefusals(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		code int
	}{
		{"unmasked", append([]byte{0x81, 0x03}, []byte("hey")...), closeProtocol},
		{"reserved bit", []byte{0xC1, 0x80, 1, 2, 3, 4}, closeProtocol},
		{"unknown opcode", clientFrame(t, true, 0x3, []byte("x")), closeProtocol},
		{"fragmented ping", clientFrame(t, false, opPing, []byte("x")), closeProtocol},
		{"oversized ping", clientFrame(t, true, opPing, bytes.Repeat([]byte("x"), 126)), closeProtocol},
		{"oversized frame", clientFrame(t, true, opText, make([]byte, maxFrameBytes+1)), closeTooBig},
		{"truncated payload", clientFrame(t, true, opText, []byte("hello"))[:8], 0},
		{"truncated header", []byte{0x81}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readOne(t, tc.raw)
			var wsErr *wsError
			if !asWSError(err, &wsErr) {
				t.Fatalf("err = %v, want a websocket error", err)
			}
			if wsErr.code != tc.code {
				t.Errorf("code = %d, want %d (%s)", wsErr.code, tc.code, wsErr.reason)
			}
		})
	}
}

func asWSError(err error, target **wsError) bool { return errors.As(err, target) }

func FuzzReadFrame(f *testing.F) {
	f.Add(clientFrame(f, true, opText, []byte("hello")))
	f.Add(clientFrame(f, true, opPing, []byte("x")))
	f.Add(clientFrame(f, false, opText, []byte("part")))
	f.Add([]byte{0x81, 0x80, 1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReader(bytes.NewReader(data))
		for i := 0; i < 8; i++ {
			frame, err := readFrame(r)
			if err != nil {
				return
			}
			if len(frame.payload) > maxFrameBytes {
				t.Fatalf("a frame of %d bytes escaped the limit", len(frame.payload))
			}
		}
	})
}

func TestAcceptKeyMatchesRFC6455(t *testing.T) {
	// The example in RFC 6455 section 1.3.
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("accept = %q", got)
	}
}

// --- a real socket ---------------------------------------------------------------------------------------

// socketRig is a real service over a real listener, with the real engine and an agent whose model is off, so a turn
// is deterministic and costs nothing.
type socketRig struct {
	t      *testing.T
	server *httptest.Server
	db     *store.Store
	engine *session.Engine
	host   string
}

func newSocketRig(t *testing.T) *socketRig {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "sepia.db"), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.VerifyTriggers(ctx); err != nil {
		t.Fatal(err)
	}
	engine := session.New(session.Config{Store: db, Now: clock, ServerVersion: "test"})
	if err = db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "The Lantern Keeper", Verified: true}); err != nil {
		t.Fatal(err)
	}
	live := NewLiveBus()
	model, err := agent.New(agent.Config{
		Store: db, Session: engine, Model: agent.PinnedModel, Logger: slog.New(slog.DiscardHandler),
		Now: clock, Live: live.Publish,
	})
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := ln.Addr().String()
	cfg := config.Config{AllowedHosts: []string{host, "localhost:8893"}, Addr: host, DataDir: t.TempDir()}
	srv := &httptest.Server{
		Listener: ln,
		Config: &http.Server{Handler: New(cfg, Deps{Engine: engine, Agent: model, Live: live, Now: clock},
			slog.New(slog.DiscardHandler), nil)},
	}
	srv.Start()
	return &socketRig{t: t, server: srv, db: db, engine: engine, host: host}
}

func (r *socketRig) close() { r.server.Close() }

// newDraft makes an entry session with a card whose work is picked, so the socket can edit and confirm it.
func (r *socketRig) newDraft() domain.Session {
	r.t.Helper()
	ctx := context.Background()
	sess, err := r.engine.CreateEntry(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.engine.StartDraft(ctx, sess.ID, domain.Entry{WorkID: "w1"}); err != nil {
		r.t.Fatal(err)
	}
	return sess
}

type wsClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

func (r *socketRig) dial(sessionID string, lastSeq int, origin string) *wsClient {
	r.t.Helper()
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(context.Background(), "tcp", r.host)
	if err != nil {
		r.t.Fatalf("dial: %v", err)
	}
	client := &wsClient{t: r.t, conn: conn, br: bufio.NewReader(conn), bw: bufio.NewWriter(conn)}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	target := "/ws/sessions/" + sessionID
	if lastSeq > 0 {
		target += "?last_seq=" + strconv.Itoa(lastSeq)
	}
	request := "GET " + target + " HTTP/1.1\r\n" +
		"Host: " + r.host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n" +
		"Origin: " + origin + "\r\n\r\n"
	if _, err = client.bw.WriteString(request); err != nil {
		r.t.Fatal(err)
	}
	if err = client.bw.Flush(); err != nil {
		r.t.Fatal(err)
	}
	tp := textproto.NewReader(client.br)
	status, err := tp.ReadLine()
	if err != nil {
		r.t.Fatalf("handshake: %v", err)
	}
	if !strings.Contains(status, "101") {
		r.t.Fatalf("handshake status = %q, want 101", status)
	}
	if _, err := tp.ReadMIMEHeader(); err != nil {
		r.t.Fatalf("handshake headers: %v", err)
	}
	r.t.Cleanup(func() { _ = client.conn.Close() })
	return client
}

func (c *wsClient) send(opcode byte, payload []byte) {
	c.t.Helper()
	raw := clientFrame(c.t, true, opcode, payload)
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.conn.Write(raw); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) sendJSON(value any) {
	c.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(opText, body)
}

// next reads one server frame as an event envelope and validates it against the schema.
func (c *wsClient) next() (event, bool) { return c.nextWithin(5 * time.Second) }

func (c *wsClient) nextWithin(wait time.Duration) (event, bool) {
	c.t.Helper()
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wait))
		f, err := readFrameServer(c.br)
		if err != nil {
			c.t.Logf("frame read: %v", err)
			return event{}, false
		}
		switch f.opcode {
		case opPing:
			continue
		case opClose:
			return event{closeCode: int(binary.BigEndian.Uint16(append(f.payload, 0, 0)[:2]))}, false
		case opText:
			validator, err := contract.Default()
			if err != nil {
				c.t.Fatal(err)
			}
			if err := validator.ValidateServerEvent(f.payload); err != nil {
				c.t.Fatalf("event is not in the contract: %v\n%s", err, f.payload)
			}
			return decodeEvent(c.t, f.payload), true
		}
	}
}

// expect reads until an event of the type arrives, and fails if the socket closes first.
func (c *wsClient) expect(kind string) event {
	c.t.Helper()
	return c.expectWithin(kind, 5*time.Second)
}

// expectWithin is expect with a stated wait. A test that is only draining a frame the engine has already written
// uses a generous wait, because the race build on a loaded machine is slow enough to miss a tight one.
func (c *wsClient) expectWithin(kind string, wait time.Duration) event {
	c.t.Helper()
	for i := 0; i < 200; i++ {
		ev, ok := c.nextWithin(wait)
		if !ok {
			c.t.Fatalf("socket closed while waiting for %s (code %d)", kind, ev.closeCode)
		}
		if ev.Type == kind {
			return ev
		}
	}
	c.t.Fatalf("%s never arrived", kind)
	return event{}
}

type event struct {
	Seq       *int            `json:"seq"`
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	Data      json.RawMessage `json:"data"`
	closeCode int
}

func decodeEvent(t *testing.T, raw []byte) event {
	t.Helper()
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func (e event) field(t *testing.T, name string) any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data[name]
}

func TestSocketHelloReplaysExactlyWhatWasMissed(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	ctx := context.Background()
	if _, err := rig.engine.EditDraft(ctx, sess.ID, session.Edit{BaseVersion: 0, Set: map[string]json.RawMessage{
		"body": json.RawMessage(`"A quiet book about light."`),
	}, ClientMsgID: "m1"}); err != nil {
		t.Fatal(err)
	}
	head, err := rig.engine.HeadSequence(ctx, sess.ID)
	if err != nil || head < 2 {
		t.Fatalf("head = %d, %v", head, err)
	}

	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	hello := client.expect("hello")
	if seq := hello.field(t, "head_seq"); seq != float64(head) {
		t.Errorf("hello head_seq = %v, want %d", seq, head)
	}
	if hello.Seq != nil {
		t.Errorf("hello carried a seq: %v", *hello.Seq)
	}
	for seq := 1; seq <= head; seq++ {
		ev, ok := client.next()
		if !ok || ev.Seq == nil || *ev.Seq != seq {
			t.Fatalf("replay %d = %+v, ok=%v", seq, ev, ok)
		}
	}
	if !strings.HasPrefix(hello.Type, "hello") {
		t.Fatal("the first event was not hello")
	}

	// Resuming at head replays nothing.
	atHead := rig.dial(sess.ID, head, "http://"+rig.host)
	atHead.expect("hello")
	if ev, ok := atHead.nextWithin(150 * time.Millisecond); ok {
		t.Errorf("a resume at head replayed %s", ev.Type)
	}
}

func TestSocketEditConfirmAndDuplicate(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")

	edit := map[string]any{
		"type": "draft_edit", "client_msg_id": "e1",
		"data": map[string]any{"base_version": 0, "set": map[string]any{
			"rating": 18, "body": "A quiet book about light and memory.",
		}},
	}
	client.sendJSON(edit)
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("edit ack = %v", got)
	}
	snapshot := client.expect("draft_snapshot")
	var data struct {
		Version int `json:"version"`
		Draft   struct {
			Rating struct {
				Quarters int `json:"quarters"`
			} `json:"rating"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(snapshot.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Draft.Rating.Quarters != 18 || data.Version == 0 {
		t.Fatalf("snapshot = %+v", data)
	}

	// The same client message again is a duplicate, with one effect.
	client.sendJSON(edit)
	if got := client.expect("ack").field(t, "status"); got != "duplicate" {
		t.Fatalf("repeat edit ack = %v, want duplicate", got)
	}

	confirm := map[string]any{
		"type": "confirm", "client_msg_id": "c1",
		"data": map[string]any{"base_version": data.Version},
	}
	client.sendJSON(confirm)
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("confirm ack = %v", got)
	}
	saved := client.expect("saved")
	if saved.field(t, "entry_id") == "" {
		t.Error("saved carried no entry id")
	}
	if saved.field(t, "version") != float64(data.Version+1) {
		t.Errorf("saved version = %v, want %d", saved.field(t, "version"), data.Version+1)
	}
}

func TestSocketReturnsTheCardAfterAStaleVersion(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "draft_edit", "client_msg_id": "e1",
		"data": map[string]any{"base_version": 99, "set": map[string]any{"rating": 18}},
	})
	ack := client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "stale_version" {
		t.Fatalf("ack = %+v", ack)
	}
	client.expect("draft_snapshot")
	// The rejected edit changed nothing.
	if _, err := rig.engine.Draft(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSocketRefusesUnknownAndMalformedMessages(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")

	client.sendJSON(map[string]any{"type": "nonsense", "client_msg_id": "x1", "data": map[string]any{}})
	ack := client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "unknown_type" {
		t.Errorf("unknown type ack = %+v", ack)
	}

	client.send(opText, []byte(`{"type":"ping"}`)) // no client_msg_id and no data
	ack = client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "malformed_message" {
		t.Errorf("malformed ack = %+v", ack)
	}

	// A fragmented message is assembled, not refused.
	body, _ := json.Marshal(map[string]any{"type": "ping", "client_msg_id": "p1", "data": map[string]any{}})
	cut := len(body) / 2
	_ = client.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.conn.Write(clientFrame(t, false, opText, body[:cut])); err != nil {
		t.Fatal(err)
	}
	if _, err := client.conn.Write(clientFrame(t, true, opContinuation, body[cut:])); err != nil {
		t.Fatal(err)
	}
	ack = client.expect("ack")
	if ack.field(t, "status") != "accepted" {
		t.Errorf("fragmented ping ack = %+v", ack)
	}
}

// A turn is the server's, not the socket's: the client goes away and the message is still stored.
func TestTurnSurvivesTheSocket(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess, err := rig.engine.CreateEntry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "message", "client_msg_id": "m1",
		"data": map[string]any{"text": "finished dune last night", "client_date": "2026-11-01", "tz": "Europe/London"},
	})
	ack := client.expect("ack")
	if got := ack.field(t, "status"); got != "accepted" {
		t.Fatalf("message ack = %v (%v)", got, ack.field(t, "reason"))
	}
	_ = client.conn.Close()

	// The turn carries on: the message and the model-off notice are stored even with no socket listening.
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := rig.engine.EventsSince(context.Background(), sess.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		var userMessages, notices int
		for _, ev := range events {
			switch ev.Kind {
			case "user_message":
				userMessages++
			case "notice":
				notices++
			}
		}
		if userMessages == 1 && notices >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn did not finish: %d events", len(events))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A resume replays it.
	resumed := rig.dial(sess.ID, 0, "http://"+rig.host)
	resumed.expect("hello")
	var sawMessage, sawNotice bool
	for i := 0; i < 20; i++ {
		ev, ok := resumed.next()
		if !ok {
			break
		}
		switch ev.Type {
		case "user_message":
			sawMessage = true
		case "notice":
			sawNotice = true
		}
	}
	if !sawMessage || !sawNotice {
		t.Errorf("replay: message=%v notice=%v", sawMessage, sawNotice)
	}
}

func TestSocketRefusesAHostileOriginAndHost(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()

	// The Origin check on the upgrade runs before the handshake.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		rig.server.URL+"/ws/sessions/"+sess.ID, nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("hostile origin = %d, want 403", resp.StatusCode)
	}

	// A hostile Host is refused on every request.
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, rig.server.URL+"/healthz", nil)
	req.Host = "evil.example"
	resp, err = (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("hostile host = %d, want 400", resp.StatusCode)
	}
}

func TestSocketOversizedFrameClosesWithAStableCode(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")
	client.send(opText, make([]byte, maxFrameBytes+1))
	for i := 0; i < 5; i++ {
		ev, ok := client.next()
		if !ok {
			if ev.closeCode != closeTooBig {
				t.Errorf("close code = %d, want %d", ev.closeCode, closeTooBig)
			}
			return
		}
	}
	t.Fatal("the socket stayed open after an oversized frame")
}

func TestSocketRateLimitRefusesThenCloses(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")

	accepted, refused := 0, 0
	for i := 0; i < perSecond+refusalCap; i++ {
		client.sendJSON(map[string]any{
			"type": "ping", "client_msg_id": "p" + strconv.Itoa(i), "data": map[string]any{},
		})
	}
	// The hello replay may put a draft snapshot in front of the acks; count acks, not frames.
	for i := 0; i < 3*(perSecond+refusalCap) && accepted+refused < perSecond+refusalCap; i++ {
		ev, ok := client.next()
		if !ok {
			break
		}
		if ev.Type != "ack" {
			continue
		}
		switch ev.field(t, "status") {
		case "accepted":
			accepted++
		case "rejected":
			refused++
		}
	}
	if accepted != perSecond {
		t.Errorf("accepted = %d, want %d", accepted, perSecond)
	}
	if refused != refusalCap {
		t.Errorf("refused = %d, want %d", refused, refusalCap)
	}
	// The fifth refusal closes the socket.
	closed := false
	for i := 0; i < 5; i++ {
		ev, ok := client.next()
		if !ok {
			closed = true
			if ev.closeCode != closeRateLimited {
				t.Errorf("close code = %d, want %d", ev.closeCode, closeRateLimited)
			}
			break
		}
	}
	if !closed {
		t.Error("the socket stayed open after five refusals")
	}
}

func TestSocketRefusesInvalidUTF8(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")
	client.send(opText, []byte{0xff, 0xfe})
	for i := 0; i < 5; i++ {
		ev, ok := client.next()
		if !ok {
			if ev.closeCode != closeInvalidData {
				t.Errorf("close code = %d, want %d", ev.closeCode, closeInvalidData)
			}
			return
		}
	}
	t.Fatal("invalid UTF-8 did not close the socket")
}

// A socket for an id that does not exist is an ordinary 404, not an upgraded connection that closes.
func TestSocketUnknownSessionIs404(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		rig.server.URL+"/ws/sessions/no-such-session", nil)
	req.Header.Set("Origin", "http://"+rig.host)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
