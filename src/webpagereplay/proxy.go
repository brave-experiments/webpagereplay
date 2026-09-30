// Copyright 2017 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const errStatus = http.StatusInternalServerError

func makeLogger(req *http.Request, quietMode bool) Logger {
	if quietMode {
		return NullLogger()
	}
	return Log().With("url", req.URL.String())
}

// fixupRequestURL adds a scheme and host to req.URL.
// Adding the scheme is necessary since RoundTrip doesn't like an empty scheme.
// Adding the host is optional, but makes req.URL print more nicely.
func fixupRequestURL(req *http.Request, scheme string) {
	req.URL.Scheme = scheme
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
}

// If |paramToIgnoreInURLPath| appears in commandline switch, remove the parameter
// before recording or find a matching for it in archive.
func processRequestURLParams(req *http.Request, paramToIgnoreInURLPath string) error {
	if paramToIgnoreInURLPath != "" {
		index := strings.LastIndex(paramToIgnoreInURLPath, "::")
		if index == -1 {
			return fmt.Errorf("invalid paramToIgnoreInURLPath value (separator \"::\" not found): %s", paramToIgnoreInURLPath)
		}
		URLPath := paramToIgnoreInURLPath[:index]
		parameterToBeRemoved := paramToIgnoreInURLPath[index+2:]
		if strings.HasPrefix(req.URL.String(), URLPath) {
			u, err := url.Parse(req.URL.String())
			if err != nil {
				return fmt.Errorf("cannot parse request URL: %s", req.URL.String())
			}
			rawQuery := u.RawQuery
			retainedQueries := []string{}
			for _, param := range strings.Split(rawQuery, "&") {
				if !strings.HasPrefix(param, parameterToBeRemoved+"=") {
					retainedQueries = append(retainedQueries, param)
				}
			}
			u.RawQuery = strings.Join(retainedQueries, "&")
			req.URL = u
		}
	}
	return nil
}

// updateDate is the basic function for date adjustment.
func updateDate(h http.Header, name string, now, oldNow time.Time) {
	val := h.Get(name)
	if val == "" {
		return
	}
	oldTime, err := http.ParseTime(val)
	if err != nil {
		return
	}
	newTime := now.Add(oldTime.Sub(oldNow))
	h.Set(name, newTime.UTC().Format(http.TimeFormat))
}

// updateDates updates "Date" header as current time and adjusts "Last-Modified"/"Expires" against it.
func updateDates(h http.Header, now time.Time) {
	oldNow, err := http.ParseTime(h.Get("Date"))
	h.Set("Date", now.UTC().Format(http.TimeFormat))
	if err != nil {
		return
	}
	updateDate(h, "Last-Modified", now, oldNow)
	updateDate(h, "Expires", now, oldNow)
}

// NewReplayingProxy constructs an HTTP proxy that replays responses from an archive.
// The proxy is listening for requests on a port that uses the given scheme (e.g., http, https).
func NewReplayingProxy(a *Archive, scheme string, quietMode bool, paramToIgnoreInURLPath string) http.Handler {
	return &replayingProxy{a, scheme, quietMode, paramToIgnoreInURLPath}
}

type replayingProxy struct {
	a                      *Archive
	scheme                 string
	quietMode              bool
	paramToIgnoreInURLPath string
}

func (proxy *replayingProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	Log().Debug("Proxy: Handling request", "method", req.Method, "url", req.URL.String())
	if req.URL.Path == "/web-page-replay-generate-200" {
		w.WriteHeader(200)
		return
	}
	if req.URL.Path == "/web-page-replay-command-exit" {
		Log().Info("Received /web-page-replay-command-exit")
		Log().Info("Shutting down")
		os.Exit(0)
		return
	}
	if req.URL.Path == "/web-page-replay-reset-replay-chronology" {
		Log().Info("Received /web-page-replay-reset-replay-chronology")
		Log().Info("Reset replay order")
		proxy.a.StartNewReplaySession()
		return
	}
	fixupRequestURL(req, proxy.scheme)
	if err := processRequestURLParams(req, proxy.paramToIgnoreInURLPath); err != nil {
		Log().Error("Error processing request URL", "error", err)
		os.Exit(-1)
		return
	}

	// WebSocket connections bypass the regular HTTP machinery entirely.
	if isWebSocketHandshake(req) {
		proxy.handleWebSocketHandshake(w, req)
		return
	}

	logger := makeLogger(req, proxy.quietMode)

	// Lookup the response in the archive.
	_, storedResp, err := proxy.a.FindRequest(req)
	if err != nil {
		logger.Warn("Proxy: FAILED to find request", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer storedResp.Body.Close()

	// Check if the stored Content-Encoding matches an encoding allowed by the client.
	// If not, transform the response body to match the client's Accept-Encoding.
	clientAE := strings.ToLower(req.Header.Get("Accept-Encoding"))
	originCE := strings.ToLower(storedResp.Header.Get("Content-Encoding"))
	if !strings.Contains(clientAE, originCE) {
		logger.Info("translating Content-Encoding", "origin", originCE, "client", clientAE)
		body, err := ioutil.ReadAll(storedResp.Body)
		if err != nil {
			logger.Error("error reading response body from archive", "error", err)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err = decompressBody(originCE, body)
		if err != nil {
			logger.Error("error decompressing response body", "error", err)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ce, err := CompressBody(clientAE, body)
		if err != nil {
			logger.Error("error recompressing response body", "error", err)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		storedResp.Header.Set("Content-Encoding", ce)
		storedResp.Body = ioutil.NopCloser(bytes.NewReader(body))
		// ContentLength has changed, so update the outgoing headers accordingly.
		if storedResp.ContentLength >= 0 {
			storedResp.ContentLength = int64(len(body))
			storedResp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
	}

	// Update dates in response header.
	updateDates(storedResp.Header, time.Now())

	// Forward the response.
	logger.Info("Proxy: SERVING response", "status", storedResp.StatusCode)
	for k, v := range storedResp.Header {
		w.Header()[k] = append([]string{}, v...)
	}
	w.WriteHeader(storedResp.StatusCode)
	if _, err := io.Copy(w, storedResp.Body); err != nil {
		logger.Error("Client response truncated", "error", err)
	}
}

// handleWebSocketHandshake replays a recorded WebSocket session: it serves
// the recorded 101 handshake (recomputing Sec-WebSocket-Accept, which is
// derived from the client's key) and then serves the recorded server-to-client
// messages in order. Client control frames are handled live.
func (proxy *replayingProxy) handleWebSocketHandshake(w http.ResponseWriter, req *http.Request) {
	logger := makeLogger(req, proxy.quietMode)
	hj, ok := w.(http.Hijacker)
	if !ok {
		logger.Error("WebSocket: ResponseWriter does not support hijacking")
		w.WriteHeader(errStatus)
		return
	}
	archivedReq, _, storedResp, err := proxy.a.FindArchivedRequest(req)
	if err != nil || storedResp == nil || storedResp.StatusCode != http.StatusSwitchingProtocols {
		logger.Warn("WebSocket: FAILED to find handshake in archive", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	clientConn, clientBrw, err := hj.Hijack()
	if err != nil {
		logger.Error("WebSocket: failed to hijack client connection", "error", err)
		return
	}

	// Serve the recorded handshake, but recompute Sec-WebSocket-Accept (it
	// is derived from the client's Sec-WebSocket-Key, which differs from the
	// recorded one) and drop negotiated extensions, which are not replayed
	// (without the extension the client sends uncompressed frames).
	resp := *storedResp
	resp.Header = storedResp.Header.Clone()
	resp.Header.Set("Sec-WebSocket-Accept", computeSecWebSocketAccept(req.Header.Get("Sec-WebSocket-Key")))
	resp.Header.Del("Sec-WebSocket-Extensions")
	resp.Body = http.NoBody
	resp.ContentLength = 0
	var respBuf bytes.Buffer
	if err := resp.Write(&respBuf); err != nil {
		logger.Error("WebSocket: failed to serialize handshake response", "error", err)
		clientConn.Close()
		return
	}
	if _, err := clientConn.Write(respBuf.Bytes()); err != nil {
		logger.Error("WebSocket: failed to send handshake response to client", "error", err)
		clientConn.Close()
		return
	}

	var msgs []ArchivedWebSocketMessage
	if archivedReq != nil {
		msgs = archivedReq.WebSocketMessages
	}
	logger.Info("WebSocket: replaying session", "url", req.URL.String(), "messages", len(msgs))
	replayWsSession(clientConn, clientBrw.Reader, msgs, logger)
}

// NewRecordingProxy constructs an HTTP proxy that records responses into an archive.
// The proxy is listening for requests on a port that uses the given scheme (e.g., http, https).
func NewRecordingProxy(a *WritableArchive, scheme string, transformers []ResponseTransformer, paramToIgnoreInURLPath string) http.Handler {
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return &recordingProxy{http.DefaultTransport.(*http.Transport), a, scheme, transformers, paramToIgnoreInURLPath}
}

type recordingProxy struct {
	tr                     *http.Transport
	a                      *WritableArchive
	scheme                 string
	transformers           []ResponseTransformer
	paramToIgnoreInURLPath string
}

func (proxy *recordingProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	Log().Debug("Proxy: Handling request", "method", req.Method, "url", req.URL.String())
	if req.URL.Path == "/web-page-replay-generate-200" {
		w.WriteHeader(200)
		return
	}
	if req.URL.Path == "/web-page-replay-command-exit" {
		Log().Info("Received /web-page-replay-command-exit")
		Log().Info("Shutting down")
		if err := proxy.a.Close(); err != nil {
			Log().Error("Error flushing archive", "error", err)
		}
		os.Exit(0)
		return
	}
	fixupRequestURL(req, proxy.scheme)
	if err := processRequestURLParams(req, proxy.paramToIgnoreInURLPath); err != nil {
		Log().Error("Error processing request URL", "error", err)
		os.Exit(-1)
		return
	}

	// WebSocket connections bypass the regular HTTP machinery entirely.
	if isWebSocketHandshake(req) {
		proxy.handleWebSocketHandshake(w, req)
		return
	}

	logger := makeLogger(req, false)
	// https://github.com/golang/go/issues/16036. Server requests always
	// have non-nil body even for GET and HEAD. This prevents http.Transport
	// from retrying requests on dead reused conns. Catapult Issue 3706.
	if req.ContentLength == 0 {
		req.Body = nil
	}

	// TODO(catapult:3742): Implement Brotli support. Remove br advertisement for now.
	ce := req.Header.Get("Accept-Encoding")
	req.Header.Set("Accept-Encoding", strings.TrimSuffix(ce, ", br"))

	// Read the entire request body (for POST) before forwarding to the server
	// so we can save the entire request in the archive.
	var requestBody []byte
	if req.Body != nil {
		var err error
		requestBody, err = ioutil.ReadAll(req.Body)
		if err != nil {
			logger.Error("read request body failed", "error", err)
			w.WriteHeader(errStatus)
			return
		}
		req.Body = ioutil.NopCloser(bytes.NewReader(requestBody))
	}

	// Make the external request.
	// If RoundTrip fails, convert the response to a 500.
	resp, err := proxy.tr.RoundTrip(req)
	if err != nil {
		logger.Error("RoundTrip failed", "error", err)
		resp = &http.Response{
			Status:     http.StatusText(errStatus),
			StatusCode: errStatus,
			Proto:      req.Proto,
			ProtoMajor: req.ProtoMajor,
			ProtoMinor: req.ProtoMinor,
			Body:       ioutil.NopCloser(bytes.NewReader(nil)),
		}
	}

	// Copy the entire response body.
	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Warn("Origin response truncated", "error", err)
	}
	resp.Body.Close()

	// Restore req body (which was consumed by RoundTrip) and record original response without transformation.
	resp.Body = ioutil.NopCloser(bytes.NewReader(responseBody))
	if req.Body != nil {
		req.Body = ioutil.NopCloser(bytes.NewReader(requestBody))
	}
	if err := proxy.a.RecordRequest(req, resp); err != nil {
		logger.Error("Failed recording request", "error", err)
	}

	// Restore req and response body which are consumed by RecordRequest.
	if req.Body != nil {
		req.Body = ioutil.NopCloser(bytes.NewReader(requestBody))
	}
	resp.Body = ioutil.NopCloser(bytes.NewReader(responseBody))

	// Transform.
	for _, t := range proxy.transformers {
		t.Transform(req, resp)
	}

	responseBodyAfterTransform, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Warn("Transformed response truncated", "error", err)
	}

	// Forward the response.
	logger.Info("Proxy: SERVING response", "status", resp.StatusCode, "bytes",
		len(responseBodyAfterTransform))
	for k, v := range resp.Header {
		w.Header()[k] = append([]string{}, v...)
	}
	w.WriteHeader(resp.StatusCode)
	if n, err := io.Copy(w, bytes.NewReader(responseBodyAfterTransform)); err != nil {
		logger.Warn("Client response truncated", "written", n, "total",
			len(responseBodyAfterTransform), "error", err)
	}
}

// handleWebSocketHandshake relays a WebSocket connection between the client
// and the origin server, recording the handshake and all messages relayed on
// the connection into the archive.
func (proxy *recordingProxy) handleWebSocketHandshake(w http.ResponseWriter, req *http.Request) {
	logger := makeLogger(req, false)
	hj, ok := w.(http.Hijacker)
	if !ok {
		logger.Error("WebSocket: ResponseWriter does not support hijacking")
		w.WriteHeader(errStatus)
		return
	}

	originConn, err := dialWsOrigin(req)
	if err != nil {
		logger.Error("WebSocket: failed to connect to origin", "error", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	// Forward the handshake request to the origin unchanged.
	// (Server requests always have a non-nil body, even for GET. Nil it out
	// to prevent req.Write from emitting a chunked body; see ServeHTTP above.)
	if req.ContentLength == 0 {
		req.Body = nil
	}
	var reqBuf bytes.Buffer
	if err := req.Write(&reqBuf); err != nil {
		logger.Error("WebSocket: failed to serialize handshake request", "error", err)
		originConn.Close()
		w.WriteHeader(errStatus)
		return
	}
	if _, err := originConn.Write(reqBuf.Bytes()); err != nil {
		logger.Error("WebSocket: failed to send handshake to origin", "error", err)
		originConn.Close()
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	// Read the origin's handshake response.
	originReader := bufio.NewReader(originConn)
	resp, err := http.ReadResponse(originReader, req)
	if err != nil {
		logger.Error("WebSocket: failed to read origin handshake response", "error", err)
		originConn.Close()
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		// The origin refused the upgrade; relay its response as-is.
		logger.Warn("WebSocket: origin refused upgrade", "status", resp.StatusCode)
		for k, v := range resp.Header {
			w.Header()[k] = append([]string{}, v...)
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		originConn.Close()
		return
	}
	// A 101 response has no body; make its serialization unambiguous.
	resp.Body = http.NoBody
	resp.ContentLength = 0

	// Record the handshake immediately, so that it survives an abrupt
	// shutdown even if the session never finishes.
	archivedReq, err := proxy.a.RecordWebSocketHandshake(req, resp)
	if err != nil {
		logger.Error("WebSocket: failed to record handshake", "error", err)
	}

	clientConn, clientBrw, err := hj.Hijack()
	if err != nil {
		logger.Error("WebSocket: failed to hijack client connection", "error", err)
		originConn.Close()
		return
	}

	// Forward the origin's 101 response to the client verbatim.
	var respBuf bytes.Buffer
	if err := resp.Write(&respBuf); err != nil {
		logger.Error("WebSocket: failed to serialize handshake response", "error", err)
		clientConn.Close()
		originConn.Close()
		return
	}
	if _, err := clientConn.Write(respBuf.Bytes()); err != nil {
		logger.Error("WebSocket: failed to send handshake response to client", "error", err)
		clientConn.Close()
		originConn.Close()
		return
	}

	// Relay and record frames until the connection is torn down.
	logger.Info("WebSocket: recording session", "url", req.URL.String())
	msgs := relayWebSocketSession(clientConn, clientBrw.Reader, originConn, originReader, time.Now())
	if archivedReq != nil {
		proxy.a.SetWebSocketMessages(archivedReq, msgs)
	}
	logger.Info("WebSocket: recorded session", "url", req.URL.String(), "messages", len(msgs))
}
