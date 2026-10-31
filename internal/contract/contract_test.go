package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract tests are the ones that let two agents work apart: every example in api/examples validates, every
// malformed variant is refused, and an unknown event from a newer server is tolerated rather than fatal.

func examples(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir("../../api/examples")
	if err != nil {
		t.Fatalf("read examples: %v", err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("../../api/examples", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = b
	}
	if len(out) < 30 {
		t.Fatalf("only %d examples; the contract should have one per message and per interaction", len(out))
	}
	return out
}

func TestEveryExampleIsInTheContract(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, raw := range examples(t) {
		switch {
		case strings.HasPrefix(name, "server-"):
			if err := v.ValidateServerEvent(raw); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case strings.HasPrefix(name, "client-"):
			if err := v.ValidateClientMessage(raw); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		default:
			t.Errorf("%s: an example must start with server- or client-", name)
		}
	}
}

// Bad: the shapes a broken or hostile peer sends are refused, and the refusal names the path.
func TestMalformedVariantsAreRefused(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base := examples(t)["server-text-delta.json"]

	cases := map[string]string{
		"not JSON":         `{"seq": 1, "type":`,
		"not an object":    `[]`,
		"wrong type":       strings.Replace(string(base), `"text_delta"`, `"text_deltaa"`, 1),
		"missing field":    `{"seq":1,"type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z"}`,
		"extra field":      `{"seq":1,"type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{"message_id":"m","text":"x"},"extra":1}`,
		"oversized string": `{"seq":1,"type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{"message_id":"m","text":"` + strings.Repeat("a", 9000) + `"}}`,
		"bad instant":      `{"seq":1,"type":"text_delta","session_id":"s","ts":"yesterday","data":{"message_id":"m","text":"x"}}`,
		"wrong seq type":   `{"seq":"1","type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{"message_id":"m","text":"x"}}`,
		"deep nesting":     strings.Repeat("[", 200) + strings.Repeat("]", 200),
		"null data":        `{"seq":1,"type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z","data":null}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := v.ValidateServerEvent([]byte(raw)); err == nil {
				t.Fatal("a malformed event was accepted")
			}
		})
	}
}

func TestMalformedClientMessagesAreRefused(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := map[string]string{
		"no client_msg_id":           `{"type":"confirm","data":{"base_version":1}}`,
		"unknown type":               `{"type":"delete_everything","client_msg_id":"c","data":{}}`,
		"message too long":           `{"type":"message","client_msg_id":"c","data":{"text":"` + strings.Repeat("a", 8001) + `","client_date":"2026-10-31","tz":"UTC"}}`,
		"bad date":                   `{"type":"message","client_msg_id":"c","data":{"text":"x","client_date":"31/10/2026","tz":"UTC"}}`,
		"confirm needs a version":    `{"type":"confirm","client_msg_id":"c","data":{}}`,
		"accept needs fields or all": `{"type":"accept_unverified","client_msg_id":"c","data":{"base_version":1}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := v.ValidateClientMessage([]byte(raw)); err == nil {
				t.Fatal("a malformed client message was accepted")
			}
		})
	}
}

// The same client message twice is one effect, and the schema must accept the retry unchanged.
func TestClientMessageRetryIsValid(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw := examples(t)["client-confirm.json"]
	for i := 0; i < 3; i++ {
		if err := v.ValidateClientMessage(raw); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
}

// Sad: an event type from a newer server is decoded and recognised as unknown, so a client can log and ignore it.
func TestUnknownEventTypeIsTolerated(t *testing.T) {
	raw := []byte(`{"seq":99,"type":"thinking_started","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{}}`)
	kind, _, err := DecodeServerEvent(raw)
	if err != nil {
		t.Fatalf("DecodeServerEvent: %v", err)
	}
	if kind != "thinking_started" {
		t.Fatalf("type = %q", kind)
	}
	if KnownServerEventType(kind) {
		t.Fatal("an unknown event type was reported as known")
	}
}

func TestDecodeRejectsATruncatedFrame(t *testing.T) {
	if _, _, err := DecodeServerEvent([]byte(`{"seq":1,"type":"hel`)); err == nil {
		t.Fatal("a truncated frame decoded without an error")
	}
	if _, _, err := DecodeServerEvent([]byte(`{"seq":1}`)); err == nil {
		t.Fatal("an event with no type decoded")
	}
}

func TestKnownVocabularies(t *testing.T) {
	for _, t2 := range []string{"hello", "draft_snapshot", "interaction_request", "saved", "queue_update"} {
		if !KnownServerEventType(t2) {
			t.Errorf("%q should be a known server event", t2)
		}
	}
	for _, t2 := range []string{"message", "confirm", "interaction_response", "ping"} {
		if !KnownClientMessageType(t2) {
			t.Errorf("%q should be a known client message", t2)
		}
	}
	if KnownServerEventType("message") {
		t.Error("a client message was reported as a server event")
	}
}

func TestErrorRegistry(t *testing.T) {
	if got := Status(CodeRatingRequired); got != 422 {
		t.Errorf("status = %d, want 422", got)
	}
	if got := Copy(CodeBodyNotYourWords); !strings.Contains(got, "your own words") && !strings.Contains(got, "what you wrote") {
		t.Errorf("copy = %q, want plain words about her own writing", got)
	}
	if !KnownErrorCode(CodeStaleVersion) {
		t.Error("stale_version should be a known code")
	}
	if KnownErrorCode("made_up_code") {
		t.Error("an invented code was reported as known")
	}
	if got := Copy("made_up_code"); got == "" {
		t.Error("an unknown code must still get the generic copy")
	}
	if got := Status("made_up_code"); got != 500 {
		t.Errorf("unknown code status = %d, want 500", got)
	}
}

func TestNoticeRegistry(t *testing.T) {
	if got := NoticeLevel(NoticeProviderDown); got != "warn" {
		t.Errorf("level = %q, want warn", got)
	}
	if got := NoticeCopy(NoticePrereadUnavailable); got == "" {
		t.Error("a notice has no copy")
	}
	if got := NoticeLevel("unknown"); got != "info" {
		t.Errorf("unknown notice level = %q, want info", got)
	}
}

// Every copy in the registry is plain: no em dashes, and it ends like a sentence.
func TestCopyIsPlain(t *testing.T) {
	for code := range errorInfo {
		copy := Copy(code)
		if strings.Contains(copy, "—") {
			t.Errorf("%s copy has an em dash: %q", code, copy)
		}
		if copy == "" || !strings.HasSuffix(copy, ".") {
			t.Errorf("%s copy = %q, want a sentence", code, copy)
		}
	}
	for code := range noticeInfo {
		copy := NoticeCopy(code)
		if strings.Contains(copy, "—") {
			t.Errorf("%s notice has an em dash: %q", code, copy)
		}
	}
}

// FuzzValidateServerEvent feeds arbitrary bytes to the schema decoder. The socket is reachable from a phone on the
// tailnet, so a malformed frame must be a refusal, never a panic and never an unbounded amount of work.
func FuzzValidateServerEvent(f *testing.F) {
	v, err := New()
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	seeds := []string{
		`{"seq":1,"type":"text_delta","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{"message_id":"m","text":"hi"}}`,
		`{"seq":null,"type":"hello","session_id":"s","ts":"2026-10-31T14:03:22Z","data":{"session":{"id":"s","kind":"entry","state":"active"},"state":"active","draft":null,"open_interactions":[],"head_seq":0,"full_state":true,"server_version":"dev"}}`,
		`{}`,
		`null`,
		`{"type":`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		err := v.ValidateServerEvent(raw)
		if err != nil {
			return // a refusal is the expected outcome for almost every input
		}
		// Anything that validates must also decode, and its type must be one we know.
		kind, _, decErr := DecodeServerEvent(raw)
		if decErr != nil {
			t.Fatalf("a valid event did not decode: %v", decErr)
		}
		if !KnownServerEventType(kind) {
			t.Fatalf("a valid event had an unknown type %q", kind)
		}
	})
}
