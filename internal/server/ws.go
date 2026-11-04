package server

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // RFC 6455 specifies SHA-1 for the handshake; it is not a security hash here
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// The WebSocket half of the protocol, written against RFC 6455 rather than taken from a library. The plan asks for
// our own frame reader to be fuzzed (docs/slices/S09-server.md), and STACK.md prefers the standard library: this is
// a server for one client with no extensions, no subprotocols and no compression, so the whole surface is the
// handshake, one frame reader, one frame writer and the close handshake.
//
// The limits are the ones in docs/STANDARDS.md 4a: a 64 KiB frame (and so a 64 KiB message, since fragmentation
// is assembled), control frames of at most 125 bytes, and a client that must mask every frame.

const (
	// maxFrameBytes bounds one frame and one assembled message.
	maxFrameBytes = 64 << 10
	// maxControlBytes is RFC 6455's limit for ping, pong and close payloads.
	maxControlBytes = 125
	// maxCloseReason is how much of a reason string a close frame can carry.
	maxCloseReason = maxControlBytes - 2
)

// Close codes. 1000 to 1011 are RFC 6455; 4000 and up are ours, and stable so a client can tell a restart from an
// abuse refusal (docs/PROTOCOL.md 1).
const (
	closeNormal      = 1000
	closeGoingAway   = 1001
	closeProtocol    = 1002
	closeUnsupported = 1003
	closeInvalidData = 1007
	closeTooBig      = 1009
	closeInternal    = 1011
	closeNotFound    = 4004
	closeRateLimited = 4008
	closeUnavailable = 4003
)

// Frame opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsHandshakeGUID is the constant RFC 6455 appends before hashing Sec-WebSocket-Key.
const wsHandshakeGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsError is a protocol failure with the close code the peer is owed. A nil code means the peer simply went away and
// no close frame should be attempted.
type wsError struct {
	code   int
	reason string
}

func (e *wsError) Error() string { return fmt.Sprintf("websocket: %s (%d)", e.reason, e.code) }

// frame is one decoded frame. payload is unmasked and owned by the caller.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// readFrame reads one client frame, enforcing the limits. A client frame that is not masked, a reserved bit, a
// control frame that is fragmented or too long, and a payload over the limit are all protocol errors with a close
// code.
func readFrame(r *bufio.Reader) (frame, error) { return readFrameMode(r, true) }

// readFrameServer reads a frame the server wrote, which RFC 6455 says is never masked. It exists for the tests that
// drive a real connection, so the client side parses with the same code the server trusts.
func readFrameServer(r *bufio.Reader) (frame, error) { return readFrameMode(r, false) }

func readFrameMode(r *bufio.Reader, requireMask bool) (frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return frame{}, &wsError{reason: "the client went away"}
		}
		return frame{}, err
	}
	fin := head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		return frame{}, &wsError{code: closeProtocol, reason: "a reserved bit was set"}
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch opcode {
	case opContinuation, opText, opBinary, opClose, opPing, opPong:
	default:
		return frame{}, &wsError{code: closeProtocol, reason: "unknown opcode"}
	}
	control := opcode&0x8 != 0
	if control && (!fin || length > maxControlBytes) {
		return frame{}, &wsError{code: closeProtocol, reason: "a fragmented or oversized control frame"}
	}
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, &wsError{reason: "a truncated length"}
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, &wsError{reason: "a truncated length"}
		}
		length = binary.BigEndian.Uint64(ext[:])
		if length>>63 != 0 {
			return frame{}, &wsError{code: closeProtocol, reason: "a negative length"}
		}
	}
	if control && length > maxControlBytes {
		return frame{}, &wsError{code: closeProtocol, reason: "an oversized control frame"}
	}
	if length > maxFrameBytes {
		return frame{}, &wsError{code: closeTooBig, reason: "a frame over 64 KiB"}
	}
	// A server must refuse an unmasked client frame; accepting one is the bug the mask exists to prevent.
	if requireMask && !masked {
		return frame{}, &wsError{code: closeProtocol, reason: "an unmasked client frame"}
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, &wsError{reason: "a truncated mask"}
		}
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return frame{}, &wsError{reason: "a truncated payload"}
		}
		if masked {
			for i := range payload {
				payload[i] ^= key[i&3]
			}
		}
	}
	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

// writeFrame writes one unmasked, unfragmented frame. bufio.Writer is single-writer by contract, and the socket
// holds its own write mutex, so a frame is never interleaved with another.
func writeFrame(w *bufio.Writer, opcode byte, payload []byte) error {
	var head [10]byte
	head[0] = 0x80 | opcode
	n := 2
	switch {
	case len(payload) < 126:
		head[1] = byte(len(payload)) //nolint:gosec // G115: the branch guarantees it is under 126
	case len(payload) <= 0xFFFF:
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:4], uint16(len(payload))) //nolint:gosec // G115: the branch guarantees it fits
		n = 4
	default:
		head[1] = 127
		binary.BigEndian.PutUint64(head[2:10], uint64(len(payload)))
		n = 10
	}
	if _, err := w.Write(head[:n]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return w.Flush()
}

// closeFrame builds the payload of a close frame: a two-byte code and at most 123 bytes of reason.
func closeFrame(code int, reason string) []byte {
	if len(reason) > maxCloseReason {
		reason = reason[:maxCloseReason]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], uint16(code)) //nolint:gosec // G115: codes are small constants
	copy(payload[2:], reason)
	return payload
}

// websocket is a hijacked connection with its buffered reader and writer.
type websocket struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

// upgrade completes the HTTP handshake and returns the raw connection. The Host and Origin checks have already run
// in the guard, which is why this function does nothing about either.
func upgrade(w http.ResponseWriter, r *http.Request) (*websocket, error) {
	if !headerHasToken(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("websocket: not an upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("websocket: unsupported version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if decoded, err := base64.StdEncoding.DecodeString(key); err != nil || len(decoded) != 16 {
		return nil, errors.New("websocket: a bad key")
	}

	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, fmt.Errorf("websocket: hijack: %w", err)
	}
	// A hijacked connection is ours: clear the server's deadlines and set our own.
	_ = conn.SetDeadline(time.Time{})
	ws := &websocket{conn: conn, br: rw.Reader, bw: rw.Writer}

	accept := acceptKey(key)
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := ws.bw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket: handshake write: %w", err)
	}
	if err := ws.bw.Flush(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket: handshake flush: %w", err)
	}
	return ws, nil
}

// acceptKey is the RFC 6455 handshake answer to Sec-WebSocket-Key.
func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsHandshakeGUID)) //nolint:gosec // RFC 6455 requires SHA-1 here
	return base64.StdEncoding.EncodeToString(sum[:])
}

// writeFrame writes one frame under the socket's single-writer rule.
func (ws *websocket) write(opcode byte, payload []byte) error {
	return writeFrame(ws.bw, opcode, payload)
}

func (ws *websocket) close() error { return ws.conn.Close() }
