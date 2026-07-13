package dashboard

import (
	"bufio"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// websocketGUID is the magic value RFC 6455 §1.3 defines for deriving
// Sec-WebSocket-Accept from a client's Sec-WebSocket-Key.
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// PushInterval is how often the dashboard pushes a fresh snapshot to
// each connected WebSocket viewer.
const PushInterval = 1500 * time.Millisecond

// writeDeadline bounds every individual frame write. Combined with
// disconnect detection in the reader goroutine, this ensures a dead
// peer is noticed within a bounded time instead of blocking the push
// loop forever on a write that will never complete.
const writeDeadline = 5 * time.Second

// maxClientFrameLength caps how large a single client-to-server frame
// payload this handler will read. The dashboard never expects
// application data from the client, only (at most) a close frame, so
// this is purely a safety bound against a misbehaving peer.
const maxClientFrameLength = 1 << 16

// WebSocket opcodes used by this handler. Only a subset of RFC 6455
// is implemented: single-frame (unfragmented) text frames
// server-to-client, and opcode detection (not full handling) of
// whatever a client might send.
const (
	opText  = 0x1
	opClose = 0x8
)

// ConnTracker is notified when a dashboard WebSocket connection is
// established and torn down. Once a connection is hijacked for the
// WebSocket upgrade, it becomes invisible to http.Server.Shutdown —
// the standard library only tracks connections it still owns. A
// ConnTracker lets the owning admin.Server keep its own record of
// open dashboard connections so it can force-close them during
// graceful shutdown instead of leaving them running past Close.
type ConnTracker interface {
	// Track registers conn as an open dashboard connection.
	Track(conn net.Conn)
	// Untrack removes conn, called once the connection is done.
	Untrack(conn net.Conn)
}

// WebSocketHandler implements the hand-rolled RFC 6455 WebSocket
// endpoint used by the dashboard's live snapshot feed. It validates
// the bearer token passed as a query parameter — browsers' native
// WebSocket API cannot set custom request headers, so the same token
// used for the REST API's Authorization header is instead passed as
// ?token=... on the upgrade request — performs the handshake, and
// then pushes periodic JSON snapshots until the client disconnects.
type WebSocketHandler struct {
	token   string
	feed    *Feed
	logger  *slog.Logger
	tracker ConnTracker     // may be nil; Track/Untrack become no-ops
	wg      *sync.WaitGroup // may be nil; Close waits on this to ensure no handler goroutine is still mid-write
}

// NewWebSocketHandler creates a WebSocketHandler that authenticates
// incoming upgrade requests against token (the same admin bearer
// token used by the REST API), serves snapshots produced by feed,
// and reports each connection's lifetime to tracker so the owning
// server can force-close it on shutdown. tracker may be nil. wg is
// incremented before each handler goroutine starts and decremented
// when it finishes; the owning server waits on it in Close so no
// handler goroutine is still mid-write after Close returns. wg may
// be nil.
func NewWebSocketHandler(token string, feed *Feed, logger *slog.Logger, tracker ConnTracker, wg *sync.WaitGroup) *WebSocketHandler {
	return &WebSocketHandler{token: token, feed: feed, logger: logger, tracker: tracker, wg: wg}
}

// ServeHTTP validates the token query parameter, performs the
// WebSocket upgrade handshake, and — once upgraded — pushes a JSON
// snapshot every PushInterval until the connection is closed.
//
// Every log statement in this handler logs only r.URL.Path, never
// r.URL.String() or r.URL.RawQuery. Since the token travels as a
// query parameter on the wire (a necessity — browsers cannot set a
// custom Authorization header on a WebSocket upgrade), logging the
// full request URL at Info level or above would leak it into logs;
// this handler never does that, at any level.
func (h *WebSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	got := r.URL.Query().Get("token")
	if !validToken(h.token, got) {
		h.logger.Warn("dashboard websocket upgrade rejected: invalid or missing token",
			slog.String(logging.ComponentKey, "dashboard"),
			slog.String("path", r.URL.Path))
		http.Error(w, `{"error":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}

	conn, err := handshake(w, r)
	if err != nil {
		h.logger.Warn("dashboard websocket handshake failed",
			slog.String(logging.ComponentKey, "dashboard"),
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		return
	}
	defer conn.Close()

	if h.tracker != nil {
		h.tracker.Track(conn)
		defer h.tracker.Untrack(conn)
	}

	if h.wg != nil {
		h.wg.Add(1)
		defer h.wg.Done()
	}

	h.logger.Info("dashboard websocket connected",
		slog.String(logging.ComponentKey, "dashboard"),
		slog.String("path", r.URL.Path))

	h.serve(conn)

	h.logger.Info("dashboard websocket disconnected",
		slog.String(logging.ComponentKey, "dashboard"),
		slog.String("path", r.URL.Path))
}

// validToken reports whether got matches token using the same
// constant-time comparison the REST API's bearer-token middleware
// uses (internal/admin.tokenMiddleware). It is duplicated locally
// rather than imported from the admin package so that this hand-
// rolled WebSocket implementation has no dependency on the REST
// handler package, and vice versa.
func validToken(token, got string) bool {
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// handshake performs the RFC 6455 server-side handshake: it validates
// the Upgrade/Connection/Sec-WebSocket-Key headers, hijacks the
// underlying connection, and writes the 101 Switching Protocols
// response with the computed Sec-WebSocket-Accept value. The caller
// owns the returned net.Conn and is responsible for closing it.
func handshake(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("websocket handshake requires GET")
	}
	if !headerContainsToken(r.Header, "Connection", "upgrade") {
		return nil, errors.New("missing or invalid Connection: Upgrade header")
	}
	if !headerContainsToken(r.Header, "Upgrade", "websocket") {
		return nil, errors.New("missing or invalid Upgrade: websocket header")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key header")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	accept := computeAccept(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"

	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write handshake response: %w", err)
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flush handshake response: %w", err)
	}

	return conn, nil
}

// computeAccept derives the Sec-WebSocket-Accept header value from a
// client's Sec-WebSocket-Key per RFC 6455 §1.3:
// base64(sha1(key + websocketGUID)).
func computeAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// headerContainsToken reports whether any comma-separated value of
// the named header contains want, compared case-insensitively, per
// how Connection/Upgrade header values are specified (RFC 7230 §6.7).
func headerContainsToken(h http.Header, name, want string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

// serve drives the push loop for a single upgraded connection. It
// starts a reader goroutine whose only job is disconnect detection —
// a closed connection, a received Close frame, or any read error all
// end it — and then writes a fresh JSON snapshot every PushInterval
// until either side signals the connection is done. Whichever side
// notices the disconnect first (a failed write, or the reader
// returning) ends serve, whose deferred conn.Close() in ServeHTTP
// unblocks the other side. This guarantees both goroutines exit and
// the connection's file descriptor is released — no leaks.
func (h *WebSocketHandler) serve(conn net.Conn) {
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readLoop(conn)
	}()

	ticker := time.NewTicker(PushInterval)
	defer ticker.Stop()

	// Push an initial snapshot immediately so a viewer isn't staring
	// at an empty page for up to a full PushInterval after connecting.
	if !h.pushOnce(conn) {
		return
	}

	for {
		select {
		case <-readerDone:
			return
		case <-ticker.C:
			if !h.pushOnce(conn) {
				return
			}
		}
	}
}

// pushOnce marshals and writes one snapshot frame, returning false if
// the write failed (the peer is gone) so the caller can stop pushing.
func (h *WebSocketHandler) pushOnce(conn net.Conn) bool {
	payload, err := json.Marshal(h.feed.Build())
	if err != nil {
		h.logger.Error("dashboard snapshot marshal failed",
			slog.String(logging.ComponentKey, "dashboard"),
			slog.String("error", err.Error()))
		return false
	}
	return writeTextFrame(conn, payload) == nil
}

// readLoop reads and discards frames from conn — this dashboard never
// expects client-to-server application data — until a Close frame
// arrives or any read error occurs (including a clean or abrupt
// close of the underlying connection), at which point it returns.
// Its sole purpose is letting serve react to a client-initiated
// disconnect without waiting for the next push's write to fail.
func readLoop(conn net.Conn) {
	br := bufio.NewReader(conn)
	for {
		opcode, _, err := readFrame(br)
		if err != nil {
			return
		}
		if opcode == opClose {
			return
		}
	}
}

// writeTextFrame writes payload as a single, unfragmented,
// server-to-client (unmasked, per RFC 6455 §5.1) WebSocket text
// frame. A write deadline bounds the call so a peer that has stopped
// reading (e.g. after an abrupt network-level disconnect that hasn't
// yet surfaced as a read error) doesn't block the push loop
// indefinitely.
func writeTextFrame(conn net.Conn, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		return err
	}
	if _, err := conn.Write(frameHeader(opText, len(payload), false)); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := conn.Write(payload)
	return err
}

// frameHeader builds the leading bytes of a WebSocket frame (the
// FIN+opcode byte and the length encoding) per RFC 6455 §5.2. masked
// is always false for the frames this handler sends, since only
// client-to-server frames are required to be masked; it's a parameter
// rather than hardcoded so frameHeader can be reused for both
// directions.
func frameHeader(opcode byte, length int, masked bool) []byte {
	first := byte(0x80) | opcode // FIN=1, no fragmentation.

	var b []byte
	switch {
	case length <= 125:
		b = []byte{first, byte(length)}
	case length <= 0xFFFF:
		b = make([]byte, 4)
		b[0] = first
		b[1] = 126
		binary.BigEndian.PutUint16(b[2:4], uint16(length))
	default:
		b = make([]byte, 10)
		b[0] = first
		b[1] = 127
		binary.BigEndian.PutUint64(b[2:10], uint64(length))
	}
	if masked {
		b[1] |= 0x80
	}
	return b
}

// readFrame reads a single (unfragmented) WebSocket frame from br and
// returns its opcode and unmasked payload, per RFC 6455 §5.2. Client
// frames are masked; readFrame unmasks them before returning. This
// dashboard doesn't act on frame content — readLoop only inspects the
// opcode — but readFrame still parses correctly so the connection's
// byte stream stays in sync if a client ever does send a frame.
func readFrame(br *bufio.Reader) (opcode byte, payload []byte, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(br, head); err != nil {
		return 0, nil, err
	}
	opcode = head[0] & 0x0f
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(br, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(br, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	if length > maxClientFrameLength {
		return 0, nil, fmt.Errorf("client frame too large: %d bytes", length)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	payload = make([]byte, length)
	if length > 0 {
		if _, err = io.ReadFull(br, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}
