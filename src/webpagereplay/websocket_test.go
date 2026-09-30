// Copyright 2025 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Frame codec tests
// ---------------------------------------------------------------------------

func TestSecWebSocketAccept(t *testing.T) {
	// Example from RFC 6455, section 1.3.
	if got, want := computeSecWebSocketAccept("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Errorf("Sec-WebSocket-Accept: got %q want %q", got, want)
	}
}

func TestIsWebSocketHandshake(t *testing.T) {
	yes := map[string]string{
		"plain":         "Connection: Upgrade\r\nUpgrade: websocket\r\n",
		"multi-token":   "Connection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n",
		"mixed case":    "Connection: upgrade\r\nUpgrade: WebSocket\r\n",
		"extra headers": "Connection: keep-alive, Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: x\r\nSec-WebSocket-Version: 13\r\n",
	}
	for name, extra := range yes {
		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader([]byte(
			"GET / HTTP/1.1\r\nHost: example.com\r\n" + extra + "\r\n"))))
		if err != nil {
			t.Fatalf("%s: ReadRequest: %v", name, err)
		}
		if !isWebSocketHandshake(req) {
			t.Errorf("%s: expected handshake", name)
		}
	}
	no := map[string]string{
		"no upgrade header":  "Connection: Upgrade\r\n",
		"other upgrade":      "Connection: Upgrade\r\nUpgrade: h2c\r\n",
		"connection missing": "Upgrade: websocket\r\n",
		"wrong token":        "Connection: keep-alive\r\nUpgrade: websocket\r\n",
	}
	for name, extra := range no {
		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader([]byte(
			"GET / HTTP/1.1\r\nHost: example.com\r\n" + extra + "\r\n"))))
		if err != nil {
			t.Fatalf("%s: ReadRequest: %v", name, err)
		}
		if isWebSocketHandshake(req) {
			t.Errorf("%s: expected no handshake", name)
		}
	}
}

// dummyConn satisfies net.Conn without touching the network. Only used for
// the Close and deadline plumbing in the relay.
type dummyConn struct {
	mu       sync.Mutex
	closed   bool
	deadline time.Time
}

func (d *dummyConn) Read(b []byte) (int, error)  { return 0, io.EOF }
func (d *dummyConn) Write(b []byte) (int, error) { return len(b), nil }
func (d *dummyConn) Close() error                { d.mu.Lock(); defer d.mu.Unlock(); d.closed = true; return nil }
func (d *dummyConn) LocalAddr() net.Addr         { return nil }
func (d *dummyConn) RemoteAddr() net.Addr        { return nil }
func (d *dummyConn) SetDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline = t
	return nil
}
func (d *dummyConn) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline = t
	return nil
}
func (d *dummyConn) SetWriteDeadline(t time.Time) error { return nil }

func buildFrameBytes(t *testing.T, fin bool, opcode int, masked bool, maskKey [4]byte, payload []byte) []byte {
	var buf bytes.Buffer
	if err := writeWsFrame(&buf, &wsFrame{Fin: fin, Opcode: opcode, Masked: masked, MaskKey: maskKey, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWsFrameRoundTrip(t *testing.T) {
	var maskKey [4]byte
	binary.BigEndian.PutUint32(maskKey[:], 0xdeadbeef)
	testCases := []wsFrame{
		{Fin: true, Opcode: WsOpcodeText, Payload: []byte("hello")},
		{Fin: true, Opcode: WsOpcodeBinary, Payload: bytes.Repeat([]byte{0x01}, 200)},   // 16-bit length
		{Fin: true, Opcode: WsOpcodeBinary, Payload: bytes.Repeat([]byte{0x02}, 70000)}, // 64-bit length
		{Fin: false, Opcode: WsOpcodeText, Payload: []byte("frag")},
		{Fin: true, Opcode: WsOpcodeContinuation, Payload: []byte("ment")},
		{Fin: true, Opcode: WsOpcodePing, Payload: []byte("pi")},
		{Fin: true, Opcode: WsOpcodeClose, Payload: []byte{0x03, 0xE8}},
		{Fin: true, Opcode: WsOpcodeText}, // empty payload
	}
	for _, f := range testCases {
		f.Masked, f.MaskKey = true, maskKey
		var buf bytes.Buffer
		if err := writeWsFrame(&buf, &f); err != nil {
			t.Fatalf("opcode %d: write: %v", f.Opcode, err)
		}
		orig := append([]byte(nil), buf.Bytes()...)
		got, err := readWsFrame(bufio.NewReader(&buf))
		if err != nil {
			t.Fatalf("opcode %d: read: %v", f.Opcode, err)
		}
		if got.Fin != f.Fin || got.Opcode != f.Opcode || !bytes.Equal(got.Payload, f.Payload) {
			t.Errorf("opcode %d: round trip mismatch: got %+v want fin=%v payload=%q",
				f.Opcode, got, f.Fin, f.Payload)
		}
		if !got.Masked || got.MaskKey != maskKey {
			t.Errorf("opcode %d: mask metadata mismatch: got masked=%v key=%v", f.Opcode, got.Masked, got.MaskKey)
		}
		// Re-writing the unmasked payload with the same mask key must produce
		// identical bytes on the wire.
		var buf2 bytes.Buffer
		if err := writeWsFrame(&buf2, got); err != nil {
			t.Fatalf("opcode %d: rewrite: %v", f.Opcode, err)
		}
		if !bytes.Equal(orig, buf2.Bytes()) {
			t.Errorf("opcode %d: re-serialization differs from original bytes", f.Opcode)
		}
		// Unmasked frames must round-trip unmasked.
		f.Masked = false
		buf.Reset()
		if err := writeWsFrame(&buf, &f); err != nil {
			t.Fatal(err)
		}
		got, err = readWsFrame(bufio.NewReader(&buf))
		if err != nil {
			t.Fatal(err)
		}
		if got.Masked {
			t.Errorf("opcode %d: expected unmasked frame", f.Opcode)
		}
	}
}

func TestWsFrameOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	// Hand-craft a header declaring a 128 MiB payload without providing it.
	hdr := []byte{0x82, 127, 0, 0, 0, 0, 0x08, 0x00, 0x00, 0x00}
	buf.Write(hdr)
	if _, err := readWsFrame(bufio.NewReader(&buf)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected payload-too-large error, got %v", err)
	}
}

func TestRelayWsFramesFragmentation(t *testing.T) {
	// A fragmented text message ("hel" + "lo"), a ping, and a close.
	input := bytes.Join([][]byte{
		buildFrameBytes(t, false, WsOpcodeText, false, [4]byte{}, []byte("hel")),
		buildFrameBytes(t, true, WsOpcodeContinuation, false, [4]byte{}, []byte("lo")),
		buildFrameBytes(t, true, WsOpcodePing, false, [4]byte{}, []byte("pi")),
		buildFrameBytes(t, true, WsOpcodeClose, false, [4]byte{}, []byte{0x03, 0xE8}),
	}, nil)

	var dst bytes.Buffer
	rec := &wsRecorder{start: time.Now()}
	relayWsFrames(WsServerToClient, rec, bufio.NewReader(bytes.NewReader(input)), &dst,
		&dummyConn{}, &dummyConn{})

	// The relayed bytes must be identical to the input.
	if !bytes.Equal(dst.Bytes(), input) {
		t.Errorf("relayed bytes differ from input")
	}
	msgs := rec.messages()
	want := []ArchivedWebSocketMessage{
		{Direction: WsServerToClient, Opcode: WsOpcodeText, Payload: []byte("hello")},
		{Direction: WsServerToClient, Opcode: WsOpcodePing, Payload: []byte("pi")},
		{Direction: WsServerToClient, Opcode: WsOpcodeClose, Payload: []byte{0x03, 0xE8}},
	}
	if len(msgs) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(msgs), len(want), msgs)
	}
	for i := range want {
		if msgs[i].Direction != want[i].Direction || msgs[i].Opcode != want[i].Opcode ||
			!bytes.Equal(msgs[i].Payload, want[i].Payload) {
			t.Errorf("message %d: got {dir=%d op=%d payload=%q} want %+v",
				i, msgs[i].Direction, msgs[i].Opcode, msgs[i].Payload, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end record + replay test
// ---------------------------------------------------------------------------

// wsTestClient is a minimal raw WebSocket client used to exercise the
// proxies, since net/http cannot send upgrade requests through a proxy.
type wsTestClient struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialWsTestClient(t *testing.T, proxyURL, targetURL string) (*wsTestClient, string) {
	t.Helper()
	target, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyURL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	// Absolute-form request URI, as a browser behind an explicit proxy sends.
	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Connection: Upgrade\r\n"+
		"Upgrade: websocket\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", targetURL, target.Host, key)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	return &wsTestClient{conn: conn, br: bufio.NewReader(conn)}, key
}

// dialWsTestClientSecure dials an HTTPS proxy server (TLS, certificate not
// verified) and sends an origin-form WebSocket handshake, as a browser does
// against wpr's HTTPS MITM listener for a wss:// connection.
func dialWsTestClientSecure(t *testing.T, serverURL, targetHostPort, path string) (*wsTestClient, string) {
	t.Helper()
	hostport := strings.TrimPrefix(serverURL, "https://")
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	conn, err := tls.Dial("tcp", hostport, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial secure proxy: %v", err)
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Connection: Upgrade\r\n"+
		"Upgrade: websocket\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", path, targetHostPort, key)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	return &wsTestClient{conn: conn, br: bufio.NewReader(conn)}, key
}

func (c *wsTestClient) readHandshakeResponse(t *testing.T) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(c.br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	return resp
}

func (c *wsTestClient) sendFrame(t *testing.T, f *wsFrame) {
	t.Helper()
	f.Masked = true
	binary.BigEndian.PutUint32(f.MaskKey[:], 0x12345678)
	if err := writeWsFrame(c.conn, f); err != nil {
		t.Fatalf("send frame: %v", err)
	}
}

func (c *wsTestClient) readFrame(t *testing.T) *wsFrame {
	t.Helper()
	f, err := readWsFrame(c.br)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

func (c *wsTestClient) closeConn() { c.conn.Close() }

// startWsOrigin starts a raw TCP server that performs a WebSocket handshake
// and then executes the given script with the hijack-equivalent connection.
func startWsOrigin(t *testing.T, script func(conn net.Conn, br *bufio.Reader) error) (string, <-chan error) {
	t.Helper()
	errs := make(chan error, 8)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		defer func() { errs <- nil }()
		conn, err := ln.Accept()
		if err != nil {
			errs <- fmt.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			errs <- fmt.Errorf("origin read request: %v", err)
			return
		}
		if !isWebSocketHandshake(req) {
			errs <- fmt.Errorf("origin did not receive a websocket handshake")
			return
		}
		// Accept the handshake.
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", computeSecWebSocketAccept(req.Header.Get("Sec-WebSocket-Key")))
		if err := script(conn, br); err != nil {
			errs <- err
		}
	}()
	return ln.Addr().String(), errs
}

func TestRecordAndReplayWebSocketSession(t *testing.T) {
	archiveFile := filepath.Join(tmpdir, "TestWebSocket.json")
	originAddr, originErrs := startWsOrigin(t, func(conn net.Conn, br *bufio.Reader) error {
		// Script: two server messages; echo the client's text message; answer
		// the client's ping with a pong; reply to the client's close with a
		// close 1000; then close.
		for _, f := range []*wsFrame{
			{Fin: true, Opcode: WsOpcodeText, Payload: []byte("hello from server")},
			{Fin: true, Opcode: WsOpcodeBinary, Payload: []byte{1, 2, 3}},
		} {
			if err := writeWsFrame(conn, f); err != nil {
				return fmt.Errorf("origin send: %v", err)
			}
		}
		frame, err := readWsFrame(br)
		if err != nil {
			return fmt.Errorf("origin read client frame: %v", err)
		}
		if frame.Opcode != WsOpcodeText || string(frame.Payload) != "hello from client" {
			return fmt.Errorf("origin got unexpected frame: op=%d payload=%q", frame.Opcode, frame.Payload)
		}
		if err := writeWsFrame(conn, &wsFrame{Fin: true, Opcode: WsOpcodeText, Payload: frame.Payload}); err != nil {
			return fmt.Errorf("origin echo: %v", err)
		}
		if frame, err = readWsFrame(br); err != nil {
			return fmt.Errorf("origin read ping: %v", err)
		}
		if frame.Opcode != WsOpcodePing || string(frame.Payload) != "ping!" {
			return fmt.Errorf("origin got unexpected ping: op=%d payload=%q", frame.Opcode, frame.Payload)
		}
		if err := writeWsFrame(conn, &wsFrame{Fin: true, Opcode: WsOpcodePong, Payload: frame.Payload}); err != nil {
			return fmt.Errorf("origin pong: %v", err)
		}
		if frame, err = readWsFrame(br); err != nil {
			return fmt.Errorf("origin read close: %v", err)
		}
		if frame.Opcode != WsOpcodeClose || closeStatusCode(frame.Payload) != 1001 {
			return fmt.Errorf("origin got unexpected close: op=%d payload=%v", frame.Opcode, frame.Payload)
		}
		writeWsFrame(conn, closeFrame(1000))
		// Wait for EOF (the client closing) before closing ourselves.
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		br.ReadByte() // discard whatever/EOF
		return nil
	})

	// --- Record ---
	recordArchive, err := OpenWritableArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	recordServer := httptest.NewServer(NewRecordingProxy(recordArchive, "http", nil, ""))

	client, key := dialWsTestClient(t, recordServer.URL, "http://"+originAddr+"/ws")
	resp := client.readHandshakeResponse(t)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status: got %d want 101", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), computeSecWebSocketAccept(key); got != want {
		t.Errorf("Sec-WebSocket-Accept: got %q want %q", got, want)
	}

	// Read the two scripted server messages.
	f1 := client.readFrame(t)
	if f1.Opcode != WsOpcodeText || string(f1.Payload) != "hello from server" {
		t.Errorf("first message: got op=%d %q", f1.Opcode, f1.Payload)
	}
	f2 := client.readFrame(t)
	if f2.Opcode != WsOpcodeBinary || !bytes.Equal(f2.Payload, []byte{1, 2, 3}) {
		t.Errorf("second message: got op=%d %v", f2.Opcode, f2.Payload)
	}
	// Send a client message, a ping, and then close. Consume the server's
	// replies in order.
	client.sendFrame(t, &wsFrame{Fin: true, Opcode: WsOpcodeText, Payload: []byte("hello from client")})
	expectText(t, client, "hello from client")
	client.sendFrame(t, &wsFrame{Fin: true, Opcode: WsOpcodePing, Payload: []byte("ping!")})
	expectOpcode(t, client, WsOpcodePong, []byte("ping!"))
	client.sendFrame(t, closeFrame(1001))
	// The origin's close reply (1000) comes back through the relay.
	f3 := client.readFrame(t)
	if f3.Opcode != WsOpcodeClose || closeStatusCode(f3.Payload) != 1000 {
		t.Errorf("close: got op=%d code=%d", f3.Opcode, closeStatusCode(f3.Payload))
	}
	client.closeConn()

	// httptest.Server.Close() does not wait for handlers that hijacked their
	// connection, so wait until the relay has finished recording the session.
	waitUntil(t, 5*time.Second, "WebSocket messages to be recorded", func() bool {
		recordArchive.mu.Lock()
		defer recordArchive.mu.Unlock()
		for _, urls := range recordArchive.Archive.Requests {
			for _, entries := range urls {
				for _, e := range entries {
					if len(e.WebSocketMessages) == 8 {
						return true
					}
				}
			}
		}
		return false
	})

	// Flush the archive and shut down the recorder.
	recordServer.Close()
	if err := recordArchive.Close(); err != nil {
		t.Fatalf("CloseArchive: %v", err)
	}
	if err := <-originErrs; err != nil {
		t.Errorf("origin server error: %v", err)
	}

	// Check the recorded archive contents.
	recordedArchive, err := OpenArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	req, _ := http.NewRequest("GET", "http://"+originAddr+"/ws", nil)
	archivedReq, _, storedResp, err := recordedArchive.FindArchivedRequest(req)
	if err != nil {
		t.Fatalf("FindArchivedRequest: %v", err)
	}
	if storedResp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("recorded handshake status: %d", storedResp.StatusCode)
	}
	// Recorded server->client messages: hello, binary, echo, pong, close.
	// Recorded client->server messages: hello, ping, close.
	var s2c, c2s int
	for _, m := range archivedReq.WebSocketMessages {
		switch m.Direction {
		case WsServerToClient:
			s2c++
		case WsClientToServer:
			c2s++
		}
	}
	if s2c != 5 || c2s != 3 {
		t.Errorf("recorded messages: got %d server->client and %d client->server, want 5 and 3: %+v",
			s2c, c2s, archivedReq.WebSocketMessages)
	}
	last := archivedReq.WebSocketMessages[len(archivedReq.WebSocketMessages)-1]
	if last.Direction != WsServerToClient || last.Opcode != WsOpcodeClose || closeStatusCode(last.Payload) != 1000 {
		t.Errorf("last recorded message: got {dir=%d op=%d payload=%v}, want server close 1000",
			last.Direction, last.Opcode, last.Payload)
	}

	// --- Replay ---
	replayServer := httptest.NewServer(NewReplayingProxy(recordedArchive, "http", false, ""))
	rc, rkey := dialWsTestClient(t, replayServer.URL, "http://"+originAddr+"/ws")
	rresp := rc.readHandshakeResponse(t)
	if rresp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("replay handshake status: got %d want 101", rresp.StatusCode)
	}
	// The accept header must be recomputed for the new client key.
	if got, want := rresp.Header.Get("Sec-WebSocket-Accept"), computeSecWebSocketAccept(rkey); got != want {
		t.Errorf("replayed Sec-WebSocket-Accept: got %q want %q", got, want)
	}

	// The recorded server->client messages must arrive, in order.
	expectText(t, rc, "hello from server")
	expectBinary(t, rc, []byte{1, 2, 3})
	expectText(t, rc, "hello from client")
	// The recorded close (1000) must terminate the session.
	expectOpcode(t, rc, WsOpcodeClose, []byte{0x03, 0xE8})
	// The server must close the connection afterwards.
	rc.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := rc.conn.Read(make([]byte, 1)); err == nil {
		t.Errorf("expected connection close after replayed close frame")
	}
	rc.closeConn()
	replayServer.Close()
}

func expectText(t *testing.T, c *wsTestClient, want string) {
	t.Helper()
	f := c.readFrame(t)
	if f.Opcode != WsOpcodeText || string(f.Payload) != want {
		t.Errorf("expected text %q, got op=%d %q", want, f.Opcode, f.Payload)
	}
}

func expectBinary(t *testing.T, c *wsTestClient, want []byte) {
	t.Helper()
	f := c.readFrame(t)
	if f.Opcode != WsOpcodeBinary || !bytes.Equal(f.Payload, want) {
		t.Errorf("expected binary %v, got op=%d %v", want, f.Opcode, f.Payload)
	}
}

func expectOpcode(t *testing.T, c *wsTestClient, opcode int, want []byte) {
	t.Helper()
	f := c.readFrame(t)
	if f.Opcode != opcode || !bytes.Equal(f.Payload, want) {
		t.Errorf("expected opcode %d payload %v, got op=%d %v", opcode, want, f.Opcode, f.Payload)
	}
}

// waitUntil polls cond until it returns true or the timeout elapses.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// If the recorded session stayed open (no recorded close), replay must keep
// the connection open, synthesize pongs for client pings, and echo closes.
func TestReplayWebSocketPingPong(t *testing.T) {
	archiveFile := filepath.Join(tmpdir, "TestWsPingPong.json")
	originAddr, originErrs := startWsOrigin(t, func(conn net.Conn, br *bufio.Reader) error {
		// Script: one server message; answer the client's ping; do not reply
		// to the client's close (just close the connection).
		if err := writeWsFrame(conn, &wsFrame{Fin: true, Opcode: WsOpcodeText, Payload: []byte("hello from server")}); err != nil {
			return fmt.Errorf("origin send: %v", err)
		}
		frame, err := readWsFrame(br)
		if err != nil {
			return fmt.Errorf("origin read ping: %v", err)
		}
		if frame.Opcode != WsOpcodePing || string(frame.Payload) != "ping!" {
			return fmt.Errorf("origin got unexpected ping: op=%d payload=%q", frame.Opcode, frame.Payload)
		}
		if err := writeWsFrame(conn, &wsFrame{Fin: true, Opcode: WsOpcodePong, Payload: frame.Payload}); err != nil {
			return fmt.Errorf("origin pong: %v", err)
		}
		if frame, err = readWsFrame(br); err != nil {
			return fmt.Errorf("origin read close: %v", err)
		}
		if frame.Opcode != WsOpcodeClose || closeStatusCode(frame.Payload) != 1001 {
			return fmt.Errorf("origin got unexpected close: op=%d payload=%v", frame.Opcode, frame.Payload)
		}
		return nil
	})

	recordArchive, err := OpenWritableArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	recordServer := httptest.NewServer(NewRecordingProxy(recordArchive, "http", nil, ""))

	client, _ := dialWsTestClient(t, recordServer.URL, "http://"+originAddr+"/ws")
	if resp := client.readHandshakeResponse(t); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status: got %d want 101", resp.StatusCode)
	}
	expectText(t, client, "hello from server")
	// A client ping is answered by the origin (relayed) during recording.
	client.sendFrame(t, &wsFrame{Fin: true, Opcode: WsOpcodePing, Payload: []byte("ping!")})
	expectOpcode(t, client, WsOpcodePong, []byte("ping!"))
	// The origin does not reply to the close; it just closes the connection.
	client.sendFrame(t, closeFrame(1001))
	client.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.conn.Read(make([]byte, 1)); err == nil {
		t.Errorf("expected connection close from relay")
	}
	client.closeConn()

	waitUntil(t, 5*time.Second, "WebSocket messages to be recorded", func() bool {
		recordArchive.mu.Lock()
		defer recordArchive.mu.Unlock()
		for _, urls := range recordArchive.Archive.Requests {
			for _, entries := range urls {
				for _, e := range entries {
					if len(e.WebSocketMessages) == 4 {
						return true
					}
				}
			}
		}
		return false
	})
	recordServer.Close()
	if err := recordArchive.Close(); err != nil {
		t.Fatalf("CloseArchive: %v", err)
	}
	if err := <-originErrs; err != nil {
		t.Errorf("origin server error: %v", err)
	}

	recordedArchive, err := OpenArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	replayServer := httptest.NewServer(NewReplayingProxy(recordedArchive, "http", false, ""))
	rc, _ := dialWsTestClient(t, replayServer.URL, "http://"+originAddr+"/ws")
	if resp := rc.readHandshakeResponse(t); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("replay handshake status: got %d want 101", resp.StatusCode)
	}
	expectText(t, rc, "hello from server")
	// A live ping must be answered with a synthesized pong (recorded pongs
	// are not replayed).
	rc.sendFrame(t, &wsFrame{Fin: true, Opcode: WsOpcodePing, Payload: []byte("ping!")})
	expectOpcode(t, rc, WsOpcodePong, []byte("ping!"))
	// A client close must be echoed, and the session terminated.
	rc.sendFrame(t, closeFrame(1001))
	expectOpcode(t, rc, WsOpcodeClose, []byte{0x03, 0xE9})
	rc.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := rc.conn.Read(make([]byte, 1)); err == nil {
		t.Errorf("expected connection close after echoed close frame")
	}
	rc.closeConn()
	replayServer.Close()
}

// The same end-to-end flow, but with a TLS origin (the wss:// case): the
// recording proxy must dial the origin over TLS.
func TestRecordAndReplayWebSocketSecureOrigin(t *testing.T) {
	archiveFile := filepath.Join(tmpdir, "TestWsSecureOrigin.json")
	scriptErrs := make(chan error, 8)
	script := func(conn net.Conn, br *bufio.Reader) error {
		// One server text message, then echo the client's close with 1000.
		if err := writeWsFrame(conn, &wsFrame{Fin: true, Opcode: WsOpcodeText, Payload: []byte("secure hello")}); err != nil {
			return fmt.Errorf("origin send: %v", err)
		}
		frame, err := readWsFrame(br)
		if err != nil {
			return fmt.Errorf("origin read close: %v", err)
		}
		if frame.Opcode != WsOpcodeClose || closeStatusCode(frame.Payload) != 1001 {
			return fmt.Errorf("origin got unexpected close: op=%d payload=%v", frame.Opcode, frame.Payload)
		}
		writeWsFrame(conn, closeFrame(1000))
		return nil
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			scriptErrs <- fmt.Errorf("origin cannot hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			scriptErrs <- fmt.Errorf("origin hijack: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n",
			computeSecWebSocketAccept(req.Header.Get("Sec-WebSocket-Key")))
		if err := script(conn, brw.Reader); err != nil {
			scriptErrs <- err
			return
		}
		scriptErrs <- nil
	}))
	defer origin.Close()

	recordArchive, err := OpenWritableArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	// The client connects to wpr's HTTPS listener (as for wss://); the
	// recording proxy then dials the https origin over TLS.
	recordServer := httptest.NewTLSServer(NewRecordingProxy(recordArchive, "https", nil, ""))

	originHostPort := strings.TrimPrefix(origin.URL, "https://")
	client, _ := dialWsTestClientSecure(t, recordServer.URL, originHostPort, "/ws")
	if resp := client.readHandshakeResponse(t); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status: got %d want 101", resp.StatusCode)
	}
	expectText(t, client, "secure hello")
	client.sendFrame(t, closeFrame(1001))
	expectOpcode(t, client, WsOpcodeClose, []byte{0x03, 0xE8})
	client.closeConn()

	waitUntil(t, 5*time.Second, "WebSocket messages to be recorded", func() bool {
		recordArchive.mu.Lock()
		defer recordArchive.mu.Unlock()
		for _, urls := range recordArchive.Archive.Requests {
			for _, entries := range urls {
				for _, e := range entries {
					// server text, server close, client close
					if len(e.WebSocketMessages) == 3 {
						return true
					}
				}
			}
		}
		return false
	})
	recordServer.Close()
	if err := recordArchive.Close(); err != nil {
		t.Fatalf("CloseArchive: %v", err)
	}
	if err := <-scriptErrs; err != nil {
		t.Errorf("origin error: %v", err)
	}

	recordedArchive, err := OpenArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	replayServer := httptest.NewTLSServer(NewReplayingProxy(recordedArchive, "https", false, ""))
	rc, _ := dialWsTestClientSecure(t, replayServer.URL, originHostPort, "/ws")
	if resp := rc.readHandshakeResponse(t); resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("replay handshake status: got %d want 101", resp.StatusCode)
	}
	expectText(t, rc, "secure hello")
	expectOpcode(t, rc, WsOpcodeClose, []byte{0x03, 0xE8})
	rc.closeConn()
	replayServer.Close()
}

// A WebSocket handshake that is not in the archive must fail with 404.
func TestReplayWebSocketHandshakeNotFound(t *testing.T) {
	archiveFile := filepath.Join(tmpdir, "TestWsNotFound.json")
	recordArchive, err := OpenWritableArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	if err := recordArchive.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := OpenArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	replayServer := httptest.NewServer(NewReplayingProxy(archive, "http", false, ""))
	client, _ := dialWsTestClient(t, replayServer.URL, "http://not-in-archive.example.com/ws")
	resp := client.readHandshakeResponse(t)
	if got, want := resp.StatusCode, http.StatusNotFound; got != want {
		t.Errorf("status: got %d want %d", got, want)
	}
	client.closeConn()
	replayServer.Close()
	os.Remove(archiveFile)
}
