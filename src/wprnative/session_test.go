// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package wprnative

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// mustDispatch marshals req, calls DispatchCall, and decodes the response
// into a generic map, failing the test if the response reports an error.
func mustDispatch(t *testing.T, method string, req any) map[string]any {
	t.Helper()
	body := marshalRequest(t, req)
	respBody := DispatchCall(method, body)
	var resp map[string]any
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("%s: malformed response JSON %q: %v", method, respBody, err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("%s: request failed: %s", method, respBody)
	}
	return resp
}

// errorResponse decodes a response into the error envelope.
type errorEnvelope struct {
	OK    bool `json:"ok"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func dispatchExpectError(t *testing.T, method string, req any) errorEnvelope {
	t.Helper()
	respBody := DispatchCall(method, marshalRequest(t, req))
	var env errorEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		t.Fatalf("%s: malformed response JSON %q: %v", method, respBody, err)
	}
	if env.OK {
		t.Fatalf("%s: expected error response, got: %s", method, respBody)
	}
	if env.Error.Code == "" {
		t.Fatalf("%s: error response missing code: %s", method, respBody)
	}
	return env
}

func marshalRequest(t *testing.T, req any) []byte {
	t.Helper()
	if req == nil {
		return []byte("{}")
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return body
}

func mustRepoFile(t *testing.T, name string) string {
	t.Helper()
	// Repo-root files live two levels up from src/wprffi.
	abs, err := filepath.Abs(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("cannot find %s at %s: %v", name, abs, err)
	}
	return abs
}

func mustDeterministicJSPath(t *testing.T) string {
	t.Helper()
	return mustRepoFile(t, "deterministic.js")
}

func startRecordSession(t *testing.T, extra map[string]any) (sessionID int64, httpPort int) {
	t.Helper()
	req := map[string]any{
		"mode":          "record",
		"host":          "127.0.0.1",
		"ports":         map[string]int{"http": 0, "https": -1, "httpsToHttp": -1},
		"injectScripts": []string{mustDeterministicJSPath(t)},
		"certFiles":     []string{mustRepoFile(t, "wpr_cert.pem"), mustRepoFile(t, "ecdsa_cert.pem")},
		"keyFiles":      []string{mustRepoFile(t, "wpr_key.pem"), mustRepoFile(t, "ecdsa_key.pem")},
	}
	for k, v := range extra {
		req[k] = v
	}
	resp := mustDispatch(t, "start", req)
	sessionID = int64(resp["session"].(float64))
	ports := resp["ports"].(map[string]any)
	httpPort = int(ports["http"].(float64))
	if httpPort <= 0 {
		t.Fatalf("expected auto-selected port > 0, got %v", ports)
	}
	if got := int(ports["https"].(float64)); got != -1 {
		t.Errorf("expected disabled https port -1, got %d", got)
	}
	return sessionID, httpPort
}

// TestRecordSessionSmoke starts a record session, requests
// /web-page-replay-generate-200, stops the session, and verifies the
// temp archive was flushed, captured, and deleted.
func TestRecordSessionSmoke(t *testing.T) {
	sessionID, httpPort := startRecordSession(t, nil)

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(httpPort) + "/web-page-replay-generate-200")
	if err != nil {
		t.Fatalf("GET generate-200: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate-200 returned %d, want 200", resp.StatusCode)
	}

	stopResp := mustDispatch(t, "stop", map[string]any{"session": sessionID})
	if length := int(stopResp["archiveLength"].(float64)); length <= 0 {
		t.Errorf("expected non-empty archive, archiveLength=%v", stopResp["archiveLength"])
	}
	if path, _ := stopResp["archivePath"].(string); path != "" {
		t.Errorf("expected empty archivePath for deleted temp file, got %q", path)
	}

	archiveResp := mustDispatch(t, "getArchive", map[string]any{"session": sessionID})
	b64, _ := archiveResp["archiveBase64"].(string)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode archiveBase64: %v", err)
	}
	if len(data) != int(archiveResp["archiveLength"].(float64)) {
		t.Errorf("archiveLength mismatch: %d vs %v", len(data), archiveResp["archiveLength"])
	}
	if len(data) == 0 {
		t.Errorf("archive is empty")
	}

	// Stop is idempotent and safe to call twice: the second stop returns
	// the same result without double-closing the archive.
	stop2 := mustDispatch(t, "stop", map[string]any{"session": sessionID})
	if length := int(stop2["archiveLength"].(float64)); length <= 0 {
		t.Errorf("second stop: expected non-empty archive, archiveLength=%v", stop2["archiveLength"])
	}

	// Unknown session.
	env := dispatchExpectError(t, "getArchive", map[string]any{"session": sessionID + 99999})
	if env.Error.Code != "SESSION_NOT_FOUND" {
		t.Errorf("unknown session: expected SESSION_NOT_FOUND, got %s", env.Error.Code)
	}
}

// TestRecordExplicitArchivePath verifies that a caller-provided archive path
// survives stop (file present, non-empty, flushed to disk).
func TestRecordExplicitArchivePath(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "recorded.wprgo")

	sessionID, httpPort := startRecordSession(t, map[string]any{"archivePath": archivePath})

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(httpPort) + "/web-page-replay-generate-200")
	if err != nil {
		t.Fatalf("GET generate-200: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("generate-200 returned %d, want 200", resp.StatusCode)
	}

	stopResp := mustDispatch(t, "stop", map[string]any{"session": sessionID})
	if got, _ := stopResp["archivePath"].(string); got != archivePath {
		t.Errorf("archivePath = %q, want %q", got, archivePath)
	}
	if length := int(stopResp["archiveLength"].(float64)); length <= 0 {
		t.Errorf("expected non-empty archive, archiveLength=%v", stopResp["archiveLength"])
	}
	fi, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive file missing after stop: %v", err)
	}
	if fi.Size() == 0 {
		t.Errorf("archive file is empty")
	}

	getResp := mustDispatch(t, "getArchive", map[string]any{"session": sessionID})
	if length := int(getResp["archiveLength"].(float64)); length != int(fi.Size()) {
		t.Errorf("getArchive length %d != file size %d", length, fi.Size())
	}
}

// TestExitHandlerDoesNotKillProcess verifies that
// /web-page-replay-command-exit triggers a graceful session stop instead of
// os.Exit (which would kill the test binary).
func TestExitHandlerDoesNotKillProcess(t *testing.T) {
	sessionID, httpPort := startRecordSession(t, nil)

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(httpPort) + "/web-page-replay-command-exit")
	if err != nil {
		// The server may close the connection while responding; that is OK
		// as long as the process survives and the session gets stopped.
		t.Logf("GET command-exit failed (tolerated): %v", err)
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// The session should now be stopped (gracefully, in the background).
	// Poll getArchive: NOT_READY until the exit handler's stop completes.
	deadline := time.Now().Add(10 * time.Second)
	for {
		respBody := DispatchCall("getArchive", marshalRequest(t, map[string]any{"session": sessionID}))
		var probe map[string]any
		if err := json.Unmarshal(respBody, &probe); err != nil {
			t.Fatalf("malformed getArchive response %q: %v", respBody, err)
		}
		if ok, _ := probe["ok"].(bool); ok {
			break // archive available: the exit handler stopped the session
		}
		code := probe["error"].(map[string]any)["code"].(string)
		if code != "NOT_READY" {
			t.Fatalf("unexpected getArchive error after exit path: %s", code)
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %d was not stopped by the exit handler", sessionID)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// getArchive should work now (record session, stopped).
	archiveResp := mustDispatch(t, "getArchive", map[string]any{"session": sessionID})
	if length := int(archiveResp["archiveLength"].(float64)); length <= 0 {
		t.Errorf("expected non-empty archive after exit-path stop, got %v", archiveResp["archiveLength"])
	}
}

// TestValidationErrors covers config validation and error codes.
func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		body     any
		wantCode string
	}{
		{"unknown method", "bogus", "{}", "INVALID_CONFIG"},
		{"malformed JSON", "start", "{not json", "INVALID_CONFIG"},
		{"bad mode", "start", map[string]any{"mode": "wat"}, "INVALID_CONFIG"},
		{"ports out of range", "start", map[string]any{"mode": "record", "ports": map[string]int{"http": -2}}, "INVALID_CONFIG"},
		{"bad constant math random", "start", map[string]any{"mode": "record", "constantMathRandomResult": 1.5}, "INVALID_CONFIG"},
		{"replay requires archivePath", "start", map[string]any{"mode": "replay"}, "INVALID_CONFIG"},
		{"timed chunk unsupported", "start", map[string]any{"mode": "record", "enableExperimentalTimedChunk": true}, "INVALID_CONFIG"},
		{"stop unknown session", "stop", map[string]any{"session": 424242}, "SESSION_NOT_FOUND"},
		{"getArchive unknown session", "getArchive", map[string]any{"session": 424242}, "SESSION_NOT_FOUND"},
		{"malformed stop JSON", "stop", "]", "INVALID_CONFIG"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := dispatchExpectError(t, tt.method, tt.body)
			if env.Error.Code != tt.wantCode {
				t.Errorf("got code %s (%s), want %s", env.Error.Code, env.Error.Message, tt.wantCode)
			}
		})
	}
}

// TestReplaySessionSmoke records a real archive, then replays it and checks
// that /web-page-replay-generate-200 still returns 200. This exercises the
// replay transformers / transformed-archive path.
func TestReplaySessionSmoke(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "recorded.wprgo")

	recID, recPort := startRecordSession(t, map[string]any{"archivePath": archivePath})
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(recPort) + "/web-page-replay-generate-200")
	if err != nil {
		t.Fatalf("GET generate-200 (record): %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	mustDispatch(t, "stop", map[string]any{"session": recID})

	// Replay with no injectScripts: the archive contains deterministic.js,
	// so the default disk injection must be skipped.
	replayResp := mustDispatch(t, "start", map[string]any{
		"mode":        "replay",
		"host":        "127.0.0.1",
		"ports":       map[string]int{"http": 0, "https": -1, "httpsToHttp": -1},
		"certFiles":   []string{mustRepoFile(t, "wpr_cert.pem"), mustRepoFile(t, "ecdsa_cert.pem")},
		"keyFiles":    []string{mustRepoFile(t, "wpr_key.pem"), mustRepoFile(t, "ecdsa_key.pem")},
		"archivePath": archivePath,
		"quietMode":   true,
	})
	replayID := int64(replayResp["session"].(float64))
	ports := replayResp["ports"].(map[string]any)
	replayPort := int(ports["http"].(float64))
	if replayPort <= 0 {
		t.Fatalf("expected auto-selected replay port > 0, got %v", ports)
	}

	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(replayPort) + "/web-page-replay-generate-200")
	if err != nil {
		t.Fatalf("GET generate-200 (replay): %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("replay generate-200 returned %d, want 200", resp.StatusCode)
	}

	// getArchive on a replay session must be rejected.
	env := dispatchExpectError(t, "getArchive", map[string]any{"session": replayID})
	if env.Error.Code != "UNSUPPORTED_MODE" {
		t.Errorf("getArchive on replay session: expected UNSUPPORTED_MODE, got %s", env.Error.Code)
	}

	mustDispatch(t, "stop", map[string]any{"session": replayID})
}
