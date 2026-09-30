// Copyright 2025 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

// Minimal RFC 6455 (WebSocket) support for record and replay.
//
// WebSocket connections cannot go through the regular HTTP proxying
// machinery: an upgrade request must be answered with a 101 response, after
// which the underlying TCP connection carries raw WebSocket frames in both
// directions. This file implements:
//   - a frame codec (read/write, masking, fragmentation assembly),
//   - handshake helpers (detection, Sec-WebSocket-Accept computation),
//   - relaying of a recorded WebSocket session (record mode),
//   - playback of a recorded session (replay mode), with live handling of
//     client control frames (ping/pong/close).

import (
	"bufio"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket frame opcodes (RFC 6455, section 5.2).
const (
	WsOpcodeContinuation = 0x0
	WsOpcodeText         = 0x1
	WsOpcodeBinary       = 0x2
	WsOpcodeClose        = 0x8
	WsOpcodePing         = 0x9
	WsOpcodePong         = 0xA
)

// Directions of WebSocket traffic relative to wpr.
const (
	WsServerToClient = 0
	WsClientToServer = 1
)

const (
	// Backstop for the closing handshake in record mode, so that a peer
	// which never completes the close cannot stall the relay forever.
	websocketCloseTimeout = 10 * time.Second
	// Upper bound for a single frame's payload, to avoid exhausting memory
	// on malformed input.
	maxWsPayloadSize = 64 << 20 // 64 MiB
)

var errWsPayloadTooLarge = errors.New("websocket frame payload too large")

// wsFrame is a single WebSocket frame. Payload is always stored unmasked;
// Masked/MaskKey describe the masking that writeWsFrame must apply when
// serializing the frame back onto the wire.
type wsFrame struct {
	Fin     bool
	Opcode  int
	Masked  bool
	MaskKey [4]byte
	Payload []byte
}

// readWsFrame reads a single frame. Reserved extension bits are ignored
// (permessage-deflate and other extensions are not supported), and the
// payload is unmasked in place if the frame was masked.
func readWsFrame(r *bufio.Reader) (*wsFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	f := &wsFrame{
		Fin:    hdr[0]&0x80 != 0,
		Opcode: int(hdr[0] & 0x0f),
		Masked: hdr[1]&0x80 != 0,
	}
	length := int64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		length = int64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		length = int64(binary.BigEndian.Uint64(b[:]))
		if length < 0 {
			return nil, fmt.Errorf("invalid websocket frame length (high bit set): %d", length)
		}
	}
	if length > maxWsPayloadSize {
		return nil, fmt.Errorf("%w: %d bytes", errWsPayloadTooLarge, length)
	}
	if f.Masked {
		if _, err := io.ReadFull(r, f.MaskKey[:]); err != nil {
			return nil, err
		}
	}
	if length > 0 {
		f.Payload = make([]byte, length)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return nil, err
		}
		if f.Masked {
			applyMaskInPlace(f.Payload, f.MaskKey)
		}
	}
	return f, nil
}

// writeWsFrame serializes the frame, applying the mask (with the frame's own
// mask key) if Masked is set. Server-to-client frames must be written with
// Masked=false; client-to-server frames with Masked=true.
func writeWsFrame(w io.Writer, f *wsFrame) error {
	var hdr [14]byte
	hdr[0] = byte(f.Opcode)
	if f.Fin {
		hdr[0] |= 0x80
	}
	length := len(f.Payload)
	idx := 2
	switch {
	case length < 126:
		hdr[1] = byte(length)
	case length <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(length))
		idx = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(length))
		idx = 10
	}
	if f.Masked {
		hdr[1] |= 0x80
		copy(hdr[idx:idx+4], f.MaskKey[:])
		idx += 4
	}
	if _, err := w.Write(hdr[:idx]); err != nil {
		return err
	}
	payload := f.Payload
	if f.Masked && len(payload) > 0 {
		payload = append([]byte(nil), payload...)
		applyMaskInPlace(payload, f.MaskKey)
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// applyMaskInPlace XORs the payload with the 4-byte mask key, in place.
func applyMaskInPlace(payload []byte, key [4]byte) {
	for i := range payload {
		payload[i] ^= key[i%4]
	}
}

// computeSecWebSocketAccept computes the value of the Sec-WebSocket-Accept
// response header from the client's Sec-WebSocket-Key
// (RFC 6455, section 4.2.2).
func computeSecWebSocketAccept(key string) string {
	h := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// isWebSocketHandshake reports whether req is a WebSocket upgrade request
// (RFC 6455, section 4.2.1). Note that WebSocket-over-HTTP/2 extended CONNECT
// requests do not carry these headers and are not handled here.
func isWebSocketHandshake(req *http.Request) bool {
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return false
	}
	// The Connection header may list several comma-separated tokens
	// (e.g. "keep-alive, Upgrade").
	for _, v := range req.Header["Connection"] {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// closeStatusCode extracts the status code from a close frame payload.
// Returns 1005 (no status code) for empty payloads.
func closeStatusCode(payload []byte) int {
	if len(payload) < 2 {
		return 1005
	}
	return int(binary.BigEndian.Uint16(payload[:2]))
}

// closeFrame builds a close frame with the given status code.
func closeFrame(statusCode int) *wsFrame {
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, uint16(statusCode))
	return &wsFrame{Fin: true, Opcode: WsOpcodeClose, Payload: payload}
}

// dialWsOrigin opens a raw connection (TCP or TLS, depending on the request
// scheme) to the origin server, to be used for relaying a WebSocket session.
// Certificate errors are ignored, mirroring the recording proxy's transport.
func dialWsOrigin(req *http.Request) (net.Conn, error) {
	port := req.URL.Port()
	if port == "" {
		if req.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(req.URL.Hostname(), port)
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if req.URL.Scheme == "https" {
		return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         req.URL.Hostname(),
		})
	}
	return dialer.Dial("tcp", addr)
}

// wsRecorder accumulates the messages relayed during a recorded WebSocket
// session. Safe for concurrent use.
type wsRecorder struct {
	mu    sync.Mutex
	msgs  []ArchivedWebSocketMessage
	start time.Time
}

func (r *wsRecorder) record(direction, opcode int, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, ArchivedWebSocketMessage{
		TimestampMs: time.Since(r.start).Milliseconds(),
		Direction:   direction,
		Opcode:      opcode,
		Payload:     append([]byte(nil), payload...),
	})
}

func (r *wsRecorder) messages() []ArchivedWebSocketMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgs
}

// relayWsFrames forwards frames from src to dst (re-serializing them
// byte-identically), recording all complete messages in rec. It returns when
// src errors or EOFs, or after a close frame plus a grace period; closer is
// invoked on return to tear down the connection in the other direction.
func relayWsFrames(direction int, rec *wsRecorder, src *bufio.Reader, dst io.Writer,
	clientConn, originConn net.Conn) {
	defer func() {
		// Unblock the pump in the other direction.
		clientConn.Close()
		originConn.Close()
	}()
	var (
		fragmented bool
		msgOpcode  int
		buf        []byte
	)
	for {
		frame, err := readWsFrame(src)
		if err != nil {
			return
		}
		if err := writeWsFrame(dst, frame); err != nil {
			return
		}
		switch frame.Opcode {
		case WsOpcodeText, WsOpcodeBinary:
			if frame.Fin {
				rec.record(direction, frame.Opcode, frame.Payload)
			} else {
				fragmented = true
				msgOpcode = frame.Opcode
				buf = append(buf[:0], frame.Payload...)
			}
		case WsOpcodeContinuation:
			if fragmented {
				buf = append(buf, frame.Payload...)
				if frame.Fin {
					rec.record(direction, msgOpcode, buf)
					fragmented = false
				}
			}
		case WsOpcodeClose:
			rec.record(direction, WsOpcodeClose, frame.Payload)
			// Give the peer a chance to complete the closing handshake so
			// that its close frame is recorded too; the deadlines prevent a
			// misbehaving peer from stalling the relay forever.
			deadline := time.Now().Add(websocketCloseTimeout)
			clientConn.SetReadDeadline(deadline)
			originConn.SetReadDeadline(deadline)
		case WsOpcodePing, WsOpcodePong:
			rec.record(direction, frame.Opcode, frame.Payload)
		}
	}
}

// relayWebSocketSession relays frames between the hijacked client connection
// and the origin connection in both directions, recording all complete
// messages. It blocks until the session ends and returns the recorded
// messages.
func relayWebSocketSession(clientConn net.Conn, clientReader *bufio.Reader,
	originConn net.Conn, originReader *bufio.Reader, start time.Time) []ArchivedWebSocketMessage {
	rec := &wsRecorder{start: start}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		relayWsFrames(WsServerToClient, rec, originReader, clientConn, clientConn, originConn)
	}()
	go func() {
		defer wg.Done()
		relayWsFrames(WsClientToServer, rec, clientReader, originConn, clientConn, originConn)
	}()
	wg.Wait()
	return rec.messages()
}

// replayWsSession serves the recorded server-to-client messages on the
// hijacked connection. Client control frames are handled live: pings are
// answered with synthesized pongs and close frames terminate the session.
// Client data frames are consumed and discarded.
func replayWsSession(conn net.Conn, reader *bufio.Reader, msgs []ArchivedWebSocketMessage, logger Logger) {
	var (
		wmu sync.Mutex
		// stop is closed once the session should end (client closed, write
		// error, or recorded close served).
		stop     = make(chan struct{})
		stopOnce sync.Once
	)
	finish := func() { stopOnce.Do(func() { close(stop) }) }
	writeFrame := func(f *wsFrame) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeWsFrame(conn, f)
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			frame, err := readWsFrame(reader)
			if err != nil {
				finish()
				return
			}
			switch frame.Opcode {
			case WsOpcodePing:
				// The WebSocket protocol requires pongs in response to pings.
				if err := writeFrame(&wsFrame{Fin: true, Opcode: WsOpcodePong, Payload: frame.Payload}); err != nil {
					finish()
					return
				}
			case WsOpcodeClose:
				// Echo the close frame, then terminate the session.
				writeFrame(&wsFrame{Fin: true, Opcode: WsOpcodeClose, Payload: frame.Payload})
				finish()
				return
			}
		}
	}()

	servedClose := false
loop:
	for _, msg := range msgs {
		if msg.Direction != WsServerToClient {
			continue // client-to-server traffic is not replayed
		}
		select {
		case <-stop:
			break loop
		default:
		}
		switch msg.Opcode {
		case WsOpcodeText, WsOpcodeBinary, WsOpcodePing:
			if err := writeFrame(&wsFrame{Fin: true, Opcode: msg.Opcode, Payload: msg.Payload}); err != nil {
				logger.Debug("WebSocket: error writing recorded message", "error", err)
				break loop
			}
		case WsOpcodeClose:
			writeFrame(&wsFrame{Fin: true, Opcode: WsOpcodeClose, Payload: msg.Payload})
			servedClose = true
			break loop
		default:
			// Recorded pong frames are not replayed; pongs are synthesized
			// in response to live client pings.
		}
	}
	if servedClose {
		// After sending a close frame the server may close the TCP connection
		// immediately (RFC 6455, section 7.1.1).
	} else {
		// The recorded session stayed open after its last message; keep the
		// connection open until the client closes it.
		<-readerDone
	}
	conn.Close()
}
