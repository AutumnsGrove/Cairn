// Package contract is the machine-readable half of docs/PROTOCOL.md: the session schema and the stable error
// codes, plus the validators the server tests, the Vitest tests and the Playwright tests all use.
//
// The schema and the codes are generated into this package by `make gen` (tools/gen), because a Go package cannot
// embed a file outside its own directory and a second hand-kept copy would drift. CI runs `make gen` and fails on
// a diff, so the contract and the code that reads it cannot disagree.
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURL is the identifier the schema declares for itself.
const schemaURL = "https://sepia.invalid/api/events.schema.json"

// Validator checks one message against the contract. It is safe for concurrent use once built.
type Validator struct {
	server *jsonschema.Schema
	client *jsonschema.Schema
}

var (
	defaultOnce sync.Once
	defaultVal  *Validator
	defaultErr  error
)

// Default returns the shared validator, compiling the schema once.
func Default() (*Validator, error) {
	defaultOnce.Do(func() { defaultVal, defaultErr = New() })
	return defaultVal, defaultErr
}

// New compiles the embedded schema and its two entry points.
func New() (*Validator, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(eventsSchema))
	if err != nil {
		return nil, fmt.Errorf("contract: the embedded schema is not valid JSON: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	// Formats are asserted: a date that is not a date is a contract failure, not a warning.
	compiler.AssertFormat()
	if addErr := compiler.AddResource(schemaURL, doc); addErr != nil {
		return nil, fmt.Errorf("contract: %w", addErr)
	}
	server, err := compiler.Compile(schemaURL + "#/$defs/serverEvent")
	if err != nil {
		return nil, fmt.Errorf("contract: server events: %w", err)
	}
	client, err := compiler.Compile(schemaURL + "#/$defs/clientMessage")
	if err != nil {
		return nil, fmt.Errorf("contract: client messages: %w", err)
	}
	return &Validator{server: server, client: client}, nil
}

// ValidateServerEvent checks a JSON server event. The error names the failing path, which is what makes a
// contract test readable.
func (v *Validator) ValidateServerEvent(raw []byte) error {
	return validate(v.server, raw)
}

// ValidateClientMessage checks a JSON client message.
func (v *Validator) ValidateClientMessage(raw []byte) error {
	return validate(v.client, raw)
}

func validate(schema *jsonschema.Schema, raw []byte) error {
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("not in the contract: %w", err)
	}
	return nil
}

// DecodeServerEvent returns the event's type and its decoded envelope, without validating it. A client uses this
// to tolerate an event type from a newer server: an unknown type is logged and ignored, never a panic.
func DecodeServerEvent(raw []byte) (string, map[string]any, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", nil, fmt.Errorf("decode event: %w", err)
	}
	if envelope.Type == "" {
		return "", nil, fmt.Errorf("decode event: no type")
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return "", nil, fmt.Errorf("decode event: %w", err)
	}
	return envelope.Type, full, nil
}

// KnownServerEventType reports whether this build knows an event type. A newer server may send one that is not
// here; the client logs it and carries on.
func KnownServerEventType(t string) bool { return serverEventTypes[t] }

// KnownClientMessageType reports whether this build knows a client message type.
func KnownClientMessageType(t string) bool { return clientMessageTypes[t] }

// serverEventTypes and clientMessageTypes are the closed vocabularies from docs/PROTOCOL.md 3 and 5.
var serverEventTypes = map[string]bool{
	"hello": true, "user_message": true, "preread": true, "activity": true,
	"assistant_start": true, "text_delta": true, "assistant_end": true,
	"tool_start": true, "tool_end": true, "interaction_request": true,
	"interaction_resolved": true, "draft_snapshot": true, "saved": true,
	"discarded": true, "ack": true, "cost_update": true, "notice": true,
	"error": true, "queue_update": true,
}

var clientMessageTypes = map[string]bool{
	"message": true, "interaction_response": true, "draft_edit": true,
	"accept_unverified": true, "include_left_out": true, "confirm": true,
	"discard": true, "stop": true, "ping": true,
}

// Status is the HTTP status for an error code.
func Status(code ErrorCode) int {
	if info, ok := errorInfo[code]; ok {
		return info.Status
	}
	return 500
}

// Copy is the plain-worded UI copy for an error code. The copy lives with the code so they cannot drift.
func Copy(code ErrorCode) string {
	if info, ok := errorInfo[code]; ok {
		return info.Copy
	}
	return "Something went wrong. Try again."
}

// KnownErrorCode reports whether a code is in the registry. A code from a newer server is shown with the generic
// copy rather than as a blank.
func KnownErrorCode(code ErrorCode) bool {
	_, ok := errorInfo[code]
	return ok
}

// NoticeLevel is "info" or "warn" for a notice code.
func NoticeLevel(code NoticeCode) string {
	if info, ok := noticeInfo[code]; ok {
		return info.Level
	}
	return "info"
}

// NoticeCopy is the plain-worded copy for a notice code.
func NoticeCopy(code NoticeCode) string {
	if info, ok := noticeInfo[code]; ok {
		return info.Copy
	}
	return ""
}
