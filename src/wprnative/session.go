// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package wprnative implements the session management and JSON dispatch
// logic for embedding WebPageReplay into host processes. It deliberately
// contains no cgo so that the logic can be unit tested in-process.
//
// JSON API (all methods):
//
//	{"ok":true, ...} on success
//	{"ok":false,"error":{"code":"<CODE>","message":"<human readable>"}}
package wprnative

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.chromium.org/webpagereplay/src/webpagereplay"
)

// Error codes returned in the "error.code" field.
const (
	codeInvalidConfig       = "INVALID_CONFIG"
	codeArchiveOpenFailed   = "ARCHIVE_OPEN_FAILED"
	codeTLSConfigFailed     = "TLS_CONFIG_FAILED"
	codeScriptLoadFailed    = "SCRIPT_LOAD_FAILED"
	codePortBindFailed      = "PORT_BIND_FAILED"
	codeArchiveFlushFailed  = "ARCHIVE_FLUSH_FAILED"
	codeSessionNotFound     = "SESSION_NOT_FOUND"
	codeSessionStopped      = "SESSION_ALREADY_STOPPED"
	codeNotReady            = "NOT_READY"
	codeUnsupportedMode     = "UNSUPPORTED_MODE"
	codeInternalError       = "INTERNAL_ERROR"
	defaultStopTimeoutMs    = 5000
	defaultStopTimeout      = time.Duration(defaultStopTimeoutMs) * time.Millisecond
	defaultInjectScriptName = "deterministic.js"
)

var Log = webpagereplay.Log

// wprError is a structured error for the JSON API.
type wprError struct {
	code    string
	message string
	// details optionally carries structured context (e.g. the scheme and
	// requested port for a bind failure).
	details map[string]any
}

func newErr(code, format string, args ...any) *wprError {
	return &wprError{code: code, message: fmt.Sprintf(format, args...)}
}

// newErrWithDetails is newErr plus structured details.
func newErrWithDetails(code string, details map[string]any, format string, args ...any) *wprError {
	return &wprError{code: code, message: fmt.Sprintf(format, args...), details: details}
}

func (e *wprError) Error() string { return e.code + ": " + e.message }

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type errorResponse struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

func (e *wprError) response() []byte {
	b, err := json.Marshal(errorResponse{
		OK:    false,
		Error: errorBody{Code: e.code, Message: e.message, Details: e.details},
	})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"INTERNAL_ERROR","message":"error encoding error response"}}`)
	}
	return b
}

// ---------------------------------------------------------------------------
// Request/response types.
// ---------------------------------------------------------------------------

// portsJSON mirrors the "ports" object of the start request. Pointer fields
// distinguish "absent" from an explicit value.
//
//	0  = auto-select a port
//	-1 = disabled
//	>0 = fixed port
type portsJSON struct {
	HTTP        *int `json:"http"`
	HTTPS       *int `json:"https"`
	HTTPSToHTTP *int `json:"httpsToHttp"`
}

// resolved returns the effective port config. When the ports object is absent
// entirely, the default is {http: 0, https: 0, httpsToHttp: -1}. When the
// object is present, unspecified fields are disabled (-1).
func (p *portsJSON) resolved() (httpPort, httpsPort, httpsToHTTPPort int) {
	if p.HTTP == nil && p.HTTPS == nil && p.HTTPSToHTTP == nil {
		return 0, 0, -1
	}
	httpPort, httpsPort, httpsToHTTPPort = -1, -1, -1
	if p.HTTP != nil {
		httpPort = *p.HTTP
	}
	if p.HTTPS != nil {
		httpsPort = *p.HTTPS
	}
	if p.HTTPSToHTTP != nil {
		httpsToHTTPPort = *p.HTTPSToHTTP
	}
	return
}

func (p *portsJSON) validate() *wprError {
	httpPort, httpsPort, httpsToHTTPPort := p.resolved()
	for _, v := range []struct {
		name  string
		value int
	}{{"http", httpPort}, {"https", httpsPort}, {"httpsToHttp", httpsToHTTPPort}} {
		if v.value < -1 {
			return newErr(codeInvalidConfig,
				"ports.%s must be -1 (disabled), 0 (auto), or a positive port number; got %d",
				v.name, v.value)
		}
	}
	return nil
}

// StartRequest is the request body for the "start" method.
type StartRequest struct {
	Mode        string    `json:"mode"` // "record" or "replay"
	ArchivePath string    `json:"archivePath"`
	Host        string    `json:"host"`
	Ports       portsJSON `json:"ports"`

	CertFiles []string `json:"certFiles"`
	KeyFiles  []string `json:"keyFiles"`
	LogLevel  string   `json:"logLevel"`

	InjectScripts          []string `json:"injectScripts"`
	ParamToIgnoreInURLPath string   `json:"paramToIgnoreInURLPath"`
	NoArchiveCertificates  bool     `json:"noArchiveCertificates"`
	// Optional constant Math.random() result in [0,1); nil means "not set".
	ConstantMathRandomResult *float64 `json:"constantMathRandomResult"`
	// Defaults: HTML injection true, JS injection false (CLI defaults).
	HTMLInjection *bool `json:"htmlInjection"`
	JSInjection   *bool `json:"jsInjection"`

	// Record-only. Not supported by the FFI layer; must be false.
	EnableExperimentalTimedChunk bool `json:"enableExperimentalTimedChunk"`

	// Replay-only options.
	ServeResponseInChronologicalSequence bool   `json:"serveResponseInChronologicalSequence"`
	DisableFuzzyURLMatching              bool   `json:"disableFuzzyURLMatching"`
	QuietMode                            bool   `json:"quietMode"`
	RulesFile                            string `json:"rulesFile"`
	InjectArchiveScripts                 bool   `json:"injectArchiveScripts"`

	// Stop behavior: if the archive path was auto-generated (temp file),
	// keep it instead of deleting it on stop.
	KeepTempFile bool `json:"keepTempFile"`
}

// stopRequest is the request body for the "stop" method.
type stopRequest struct {
	Session   int64 `json:"session"`
	TimeoutMs int   `json:"timeoutMs"`
}

// getArchiveRequest is the request body for the "getArchive" method.
type getArchiveRequest struct {
	Session int64 `json:"session"`
}

// destroyRequest is the request body for the "destroy" method.
type destroyRequest struct {
	Session int64 `json:"session"`
}

type portsResponse struct {
	HTTP        int `json:"http"`
	HTTPS       int `json:"https"`
	HTTPSToHTTP int `json:"httpsToHttp"`
}

type startResponse struct {
	OK      bool          `json:"ok"`
	Session int64         `json:"session"`
	Ports   portsResponse `json:"ports"`
}

type stopResponse struct {
	OK            bool   `json:"ok"`
	Session       int64  `json:"session"`
	ArchiveLength int    `json:"archiveLength"`
	ArchivePath   string `json:"archivePath"`
}

type destroyResponse struct {
	OK      bool  `json:"ok"`
	Session int64 `json:"session"`
}

type getArchiveResponse struct {
	OK            bool   `json:"ok"`
	Session       int64  `json:"session"`
	ArchiveBase64 string `json:"archiveBase64"`
	ArchiveLength int    `json:"archiveLength"`
}

// ---------------------------------------------------------------------------
// Session registry.
// ---------------------------------------------------------------------------

type session struct {
	id   int64
	mode string // "record" or "replay"

	archivePath  string // resolved archive path ("" after temp-file cleanup)
	tempArchive  bool
	keepTempFile bool

	writable     *webpagereplay.WritableArchive // record mode only
	servers      *webpagereplay.ServerSet
	mu           sync.Mutex
	archiveBytes []byte
	flushError   string // set when flushing the archive on stop failed

	// stoppedCh is closed exactly once, when the stop sequence has fully
	// completed (servers shut down, archive flushed and captured).
	stopOnce  sync.Once
	stoppedCh chan struct{}
}

var (
	regMu         sync.Mutex
	sessionsReg         = map[int64]*session{}
	nextSessionID int64 = 1
)

func registerSession(s *session) int64 {
	regMu.Lock()
	defer regMu.Unlock()
	id := nextSessionID
	nextSessionID++
	s.id = id
	sessionsReg[id] = s
	return id
}

func lookupSession(id int64) (*session, bool) {
	regMu.Lock()
	defer regMu.Unlock()
	s, ok := sessionsReg[id]
	return s, ok
}

// ---------------------------------------------------------------------------
// Dispatch.
// ---------------------------------------------------------------------------

// DispatchCall is the pure-Go entry point of the FFI layer: it takes a method
// name and a JSON request body and returns a JSON response body. Errors are
// encoded in the response ({"ok":false,"error":{...}}), never as panics.
func DispatchCall(method string, requestJSON []byte) (response []byte) {
	defer func() {
		if r := recover(); r != nil {
			response = newErr(codeInternalError, "panic while handling %q: %v", method, r).response()
		}
	}()
	var err *wprError
	switch method {
	case "start":
		response, err = handleStart(requestJSON)
	case "stop":
		response, err = handleStop(requestJSON)
	case "getArchive":
		response, err = handleGetArchive(requestJSON)
	case "destroy":
		response, err = handleDestroy(requestJSON)
	default:
		err = newErr(codeInvalidConfig,
			"unknown method %q (expected \"start\", \"stop\", \"getArchive\", or \"destroy\")", method)
	}
	if err != nil {
		return err.response()
	}
	return response
}

func handleStart(requestJSON []byte) ([]byte, *wprError) {
	var req StartRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return nil, newErr(codeInvalidConfig, "malformed start request JSON: %v", err)
	}
	if req.Mode != "record" && req.Mode != "replay" {
		return nil, newErr(codeInvalidConfig,
			"mode must be \"record\" or \"replay\"; got %q", req.Mode)
	}
	if req.Mode == "replay" && req.ArchivePath == "" {
		return nil, newErr(codeInvalidConfig, "archivePath is required in replay mode")
	}
	if err := req.Ports.validate(); err != nil {
		return nil, err
	}
	if req.EnableExperimentalTimedChunk {
		return nil, newErr(codeInvalidConfig,
			"enableExperimentalTimedChunk is not supported by this embedding API")
	}
	if req.ConstantMathRandomResult != nil {
		v := *req.ConstantMathRandomResult
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.0 || v >= 1.0 {
			return nil, newErr(codeInvalidConfig,
				"constantMathRandomResult must be in [0, 1); got %v", v)
		}
	}
	logLevel := req.LogLevel
	if logLevel == "" {
		logLevel = "INFO"
	}
	if err := webpagereplay.SetLogLevel(logLevel); err != nil {
		return nil, newErr(codeInvalidConfig, "invalid logLevel (%s): %v", logLevel, err)
	}

	// Load root certificates (CLI default files when none are given).
	rootCerts, err := loadRootCerts(req.CertFiles, req.KeyFiles)
	if err != nil {
		return nil, err
	}

	htmlInjection := true
	if req.HTMLInjection != nil {
		htmlInjection = *req.HTMLInjection
	}
	jsInjection := false
	if req.JSInjection != nil {
		jsInjection = *req.JSInjection
	}

	host := req.Host
	if host == "" {
		host = "localhost"
	}

	var s *session
	if req.Mode == "record" {
		s, err = startRecord(&req, rootCerts, host, htmlInjection, jsInjection)
	} else {
		s, err = startReplay(&req, rootCerts, host, htmlInjection, jsInjection)
	}
	if err != nil {
		return nil, err
	}

	id := registerSession(s)
	resp := startResponse{
		OK:      true,
		Session: id,
		Ports: portsResponse{
			HTTP:        s.servers.ActualHTTPPort(),
			HTTPS:       s.servers.ActualHTTPSPort(),
			HTTPSToHTTP: s.servers.ActualHTTPSecureProxyPort(),
		},
	}
	b, jsonErr := json.Marshal(resp)
	if jsonErr != nil {
		return nil, newErr(codeInternalError, "error encoding start response: %v", jsonErr)
	}
	return b, nil
}

func loadRootCerts(certFiles, keyFiles []string) ([]tls.Certificate, *wprError) {
	if len(certFiles) == 0 && len(keyFiles) == 0 {
		// Mirror the CLI default: wpr_cert.pem,ecdsa_cert.pem /
		// wpr_key.pem,ecdsa_key.pem, resolved as given (relative to the
		// host process's working directory).
		certFiles = strings.Split("wpr_cert.pem,ecdsa_cert.pem", ",")
		keyFiles = strings.Split("wpr_key.pem,ecdsa_key.pem", ",")
	}
	if len(certFiles) != len(keyFiles) {
		return nil, newErr(codeInvalidConfig,
			"list of cert files given should match list of key files")
	}
	var rootCerts []tls.Certificate
	for i := 0; i < len(certFiles); i++ {
		Log().Info("Loading cert", "path", certFiles[i])
		Log().Info("Loading key", "path", keyFiles[i])
		rootCert, err := tls.LoadX509KeyPair(certFiles[i], keyFiles[i])
		if err != nil {
			return nil, newErr(codeTLSConfigFailed,
				"error opening cert or key files (%s, %s): %v", certFiles[i], keyFiles[i], err)
		}
		rootCerts = append(rootCerts, rootCert)
	}
	return rootCerts, nil
}

// scriptInjectorConfig mirrors the script-injection parts of CommonConfig.
type scriptInjectorConfig struct {
	htmlInjection bool
	jsInjection   bool
	transformers  []webpagereplay.ResponseTransformer
}

// addScriptFromFile loads an injected script from disk, records it in the
// archive's InjectedScripts map (checking for duplicate names), replaces the
// WPR constants, and installs a script injector transformer.
func addScriptFile(cfg *scriptInjectorConfig, scripts map[string]string, scriptFile string, timeSeedMs int64, constantMathRandomResult *float64) *wprError {
	script, err := os.ReadFile(scriptFile)
	if err != nil {
		return newErr(codeScriptLoadFailed, "error opening script %s: %v", scriptFile, err)
	}
	name := filepath.Base(scriptFile)
	if _, ok := scripts[name]; ok {
		return newErr(codeScriptLoadFailed, "Duplicate script name: %s", name)
	}
	scripts[name] = string(script)
	script = webpagereplay.ReplaceConstants(name, script, timeSeedMs, constantMathRandomResult)
	return addScriptInjector(cfg, script, name)
}

func addScriptInjector(cfg *scriptInjectorConfig, script []byte, scriptFile string) *wprError {
	Log().Info("Processing script", "path", scriptFile)
	si, err := webpagereplay.NewScriptInjector(script, webpagereplay.ScriptInjectorConfig{
		HtmlInjection: cfg.htmlInjection,
		JsInjection:   cfg.jsInjection,
	})
	if err != nil {
		return newErr(codeScriptLoadFailed,
			"error creating script injector for %s: %v", scriptFile, err)
	}
	cfg.transformers = append(cfg.transformers, si)
	return nil
}

// ---------------------------------------------------------------------------
// start (record).
// ---------------------------------------------------------------------------

func startRecord(req *StartRequest, rootCerts []tls.Certificate, host string, htmlInjection, jsInjection bool) (*session, *wprError) {
	archivePath := req.ArchivePath
	tempArchive := false
	if archivePath == "" {
		// Use a temp file when no archive path is given.
		tmp, err := os.CreateTemp("", "wpr-*.wprgo")
		if err != nil {
			return nil, newErr(codeArchiveOpenFailed, "error creating temp archive file: %v", err)
		}
		archivePath = tmp.Name()
		tmp.Close()
		tempArchive = true
	}
	writable, err := webpagereplay.OpenWritableArchive(archivePath)
	if err != nil {
		if tempArchive {
			os.Remove(archivePath)
		}
		return nil, newErr(codeArchiveOpenFailed, "could not open archive %s: %v", archivePath, err)
	}

	s := &session{
		mode:         "record",
		archivePath:  archivePath,
		tempArchive:  req.ArchivePath == "",
		keepTempFile: req.KeepTempFile,
		writable:     writable,
		stoppedCh:    make(chan struct{}),
	}
	cfg := &scriptInjectorConfig{htmlInjection: htmlInjection, jsInjection: jsInjection}

	// Mirror ProcessInjectedScriptsForRecording (minus cli.Context).
	archive := &writable.Archive
	archive.DeterministicTimeSeedMs = 1000 * time.Now().Unix()
	archive.ConstantMathRandomResult = req.ConstantMathRandomResult
	if archive.InjectedScripts == nil {
		archive.InjectedScripts = map[string]string{}
	}
	scripts := archive.InjectedScripts
	injectScripts := req.InjectScripts
	if len(injectScripts) == 0 {
		// Mirror the CLI default --inject-scripts value.
		injectScripts = []string{defaultInjectScriptName}
	}
	for _, scriptFile := range injectScripts {
		if err := addScriptFile(cfg, scripts, scriptFile, archive.DeterministicTimeSeedMs, archive.ConstantMathRandomResult); err != nil {
			writable.Close()
			if s.tempArchive && !s.keepTempFile {
				os.Remove(archivePath)
			}
			return nil, err
		}
	}

	tlsConfig, err := webpagereplay.RecordTLSConfig(rootCerts, writable, !req.NoArchiveCertificates)
	if err != nil {
		writable.Close()
		if s.tempArchive && !s.keepTempFile {
			os.Remove(archivePath)
		}
		return nil, newErr(codeTLSConfigFailed, "error creating TLSConfig: %v", err)
	}

	httpHandler := webpagereplay.NewRecordingProxyWithExit(
		writable, "http", cfg.transformers, req.ParamToIgnoreInURLPath, s.proxyExitHandler)
	httpsHandler := webpagereplay.NewRecordingProxyWithExit(
		writable, "https", cfg.transformers, req.ParamToIgnoreInURLPath, s.proxyExitHandler)

	servers, startErr := webpagereplay.StartServers(webpagereplay.ServerConfig{
		Host:                host,
		HTTPPort:            httpPortOf(req),
		HTTPSPort:           httpsPortOf(req),
		HTTPSecureProxyPort: httpsToHTTPPortOf(req),
		TLSConfig:           tlsConfig,
		HTTPHandler:         httpHandler,
		HTTPSHandler:        httpsHandler,
	})
	if startErr != nil {
		writable.Close()
		if s.tempArchive && !s.keepTempFile {
			os.Remove(archivePath)
		}
		return nil, newErrWithDetails(codePortBindFailed, portBindDetails(startErr), "%v", startErr)
	}
	s.servers = servers
	Log().Info("Opened archive", "path", archivePath)
	return s, nil
}

// ---------------------------------------------------------------------------
// start (replay).
// ---------------------------------------------------------------------------

func startReplay(req *StartRequest, rootCerts []tls.Certificate, host string, htmlInjection, jsInjection bool) (*session, *wprError) {
	if req.ArchivePath == "" {
		return nil, newErr(codeInvalidConfig, "archivePath is required in replay mode")
	}
	Log().Info("Loading archive", "path", req.ArchivePath)
	archive, err := webpagereplay.OpenArchive(req.ArchivePath)
	if err != nil {
		return nil, newErr(codeArchiveOpenFailed, "error opening archive file %s: %v", req.ArchivePath, err)
	}
	Log().Info("Opened archive", "path", req.ArchivePath)

	archive.ServeResponseInChronologicalSequence = req.ServeResponseInChronologicalSequence
	archive.DisableFuzzyURLMatching = req.DisableFuzzyURLMatching
	if archive.DisableFuzzyURLMatching {
		Log().Info("Disabling fuzzy URL matching")
	}

	s := &session{
		mode:         "replay",
		archivePath:  req.ArchivePath,
		keepTempFile: true, // replay never deletes the archive
		stoppedCh:    make(chan struct{}),
	}
	cfg := &scriptInjectorConfig{htmlInjection: htmlInjection, jsInjection: jsInjection}

	// Mirror ProcessInjectedScriptsForReplay (minus cli.Context).
	var timeSeedMs int64
	if archive.DeterministicTimeSeedMs != 0 {
		timeSeedMs = archive.DeterministicTimeSeedMs
	} else {
		// Old archive, predating the addition of DeterministicTimeSeedMs.
		timeSeedMs = 1000 * time.Now().Unix()
	}

	constantMathRandomResult := archive.ConstantMathRandomResult
	if req.ConstantMathRandomResult != nil {
		// Warn if archive contains a value that differs from the request.
		if archive.ConstantMathRandomResult != nil &&
			*req.ConstantMathRandomResult != *archive.ConstantMathRandomResult {
			Log().Warn("constant-math-random-result differs from archive",
				"request", *req.ConstantMathRandomResult, "archive", *archive.ConstantMathRandomResult)
		}
		// Still respect the requester's wishes.
		constantMathRandomResult = req.ConstantMathRandomResult
	}

	injectScripts := req.InjectScripts
	// If the user didn't explicitly request a script, and the archive already
	// contains 'deterministic.js', we skip loading the default
	// 'deterministic.js' from the file system (mirrors the CLI logic).
	if len(injectScripts) == 0 {
		if _, ok := archive.InjectedScripts[defaultInjectScriptName]; ok {
			Log().Info("Archive contains deterministic.js, skipping default injection")
			injectScripts = nil
		} else {
			injectScripts = []string{defaultInjectScriptName}
		}
	}

	scriptsMap := make(map[string]string)
	if req.InjectArchiveScripts && len(archive.InjectedScripts) > 0 {
		for name, contents := range archive.InjectedScripts {
			scriptsMap[name] = contents
			replacedContents := webpagereplay.ReplaceConstants(
				name, []byte(contents), timeSeedMs, constantMathRandomResult)
			if err := addScriptInjector(cfg, replacedContents, name); err != nil {
				return nil, err
			}
		}
	}

	// Scripts injected from the file system. Names must not collide with
	// scripts already injected from the archive.
	for _, scriptFile := range injectScripts {
		if err := addScriptFile(cfg, scriptsMap, scriptFile, timeSeedMs, constantMathRandomResult); err != nil {
			return nil, err
		}
	}

	// Rules file support (mirrors --rules-file).
	if req.RulesFile != "" {
		t, err := webpagereplay.NewRuleBasedTransformerFromFile(req.RulesFile)
		if err != nil {
			return nil, newErr(codeScriptLoadFailed, "error loading rules file %s: %v", req.RulesFile, err)
		}
		cfg.transformers = append(cfg.transformers, t)
		Log().Info("Loaded replay rules", "path", req.RulesFile)
	}

	// When recording, transformations are applied at request time, because
	// that's the only way. But here, when replaying, transformations are
	// applied ahead of requests, for performance reasons.
	transformedArchive := webpagereplay.Archive{
		Requests:                             make(map[string]map[string][]*webpagereplay.ArchivedRequest),
		Certs:                                archive.Certs,
		NegotiatedProtocol:                   archive.NegotiatedProtocol,
		DeterministicTimeSeedMs:              archive.DeterministicTimeSeedMs,
		ServeResponseInChronologicalSequence: archive.ServeResponseInChronologicalSequence,
		CurrentSessionId:                     archive.CurrentSessionId,
		DisableFuzzyURLMatching:              archive.DisableFuzzyURLMatching,
	}
	err = archive.ForEach(func(req *http.Request, resp *http.Response) error {
		for _, t := range cfg.transformers {
			t.Transform(req, resp)
		}
		return transformedArchive.AddArchivedRequest(req, resp, webpagereplay.AddModeAppend)
	})
	if err != nil {
		Log().Error("Error while creating transformed archive", "error", err)
	} else {
		archive = &transformedArchive
	}

	tlsConfig, err := webpagereplay.ReplayTLSConfig(rootCerts, archive, !req.NoArchiveCertificates)
	if err != nil {
		return nil, newErr(codeTLSConfigFailed, "error creating TLSConfig: %v", err)
	}

	httpHandler := webpagereplay.NewReplayingProxyWithExit(
		archive, "http", req.QuietMode, req.ParamToIgnoreInURLPath, s.proxyExitHandler)
	httpsHandler := webpagereplay.NewReplayingProxyWithExit(
		archive, "https", req.QuietMode, req.ParamToIgnoreInURLPath, s.proxyExitHandler)

	servers, startErr := webpagereplay.StartServers(webpagereplay.ServerConfig{
		Host:                host,
		HTTPPort:            httpPortOf(req),
		HTTPSPort:           httpsPortOf(req),
		HTTPSecureProxyPort: httpsToHTTPPortOf(req),
		TLSConfig:           tlsConfig,
		HTTPHandler:         httpHandler,
		HTTPSHandler:        httpsHandler,
	})
	if startErr != nil {
		return nil, newErrWithDetails(codePortBindFailed, portBindDetails(startErr), "%v", startErr)
	}
	s.servers = servers
	return s, nil
}

func httpPortOf(req *StartRequest) int {
	p, _, _ := req.Ports.resolved()
	return p
}

func httpsPortOf(req *StartRequest) int {
	_, p, _ := req.Ports.resolved()
	return p
}

func httpsToHTTPPortOf(req *StartRequest) int {
	_, _, p := req.Ports.resolved()
	return p
}

// proxyExitHandler returns the exit handler wired into the proxies: instead of
// os.Exit killing the host process, the /web-page-replay-command-exit path
// triggers this session's graceful stop in a background goroutine.
func (s *session) proxyExitHandler(exitCode int) {
	if exitCode != 0 {
		Log().Error("Error processing request URL in proxy; stopping session", "session", s.id, "exitCode", exitCode)
	}
	go s.stop(defaultStopTimeout)
}

// ---------------------------------------------------------------------------
// stop.
// ---------------------------------------------------------------------------

func handleStop(requestJSON []byte) ([]byte, *wprError) {
	var req stopRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return nil, newErr(codeInvalidConfig, "malformed stop request JSON: %v", err)
	}
	s, ok := lookupSession(req.Session)
	if !ok {
		return nil, newErr(codeSessionNotFound, "no session with id %d", req.Session)
	}
	timeout := defaultStopTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	s.stop(timeout)

	s.mu.Lock()
	archiveLength := len(s.archiveBytes)
	archivePath := s.archivePath
	flushError := s.flushError
	s.mu.Unlock()
	if flushError != "" {
		return nil, newErr(codeArchiveFlushFailed,
			"session %d stopped, but flushing the archive failed: %s", req.Session, flushError)
	}

	resp := stopResponse{
		OK:            true,
		Session:       req.Session,
		ArchiveLength: archiveLength,
		ArchivePath:   archivePath,
	}
	b, jsonErr := json.Marshal(resp)
	if jsonErr != nil {
		return nil, newErr(codeInternalError, "error encoding stop response: %v", jsonErr)
	}
	return b, nil
}

// stop gracefully shuts down the session's servers, closes and flushes the
// archive (record mode) and captures the archive bytes. It is idempotent and
// safe to call from multiple goroutines: the first call performs the shutdown
// sequence, concurrent/later calls block until it completes and report
// already-stopped.
func (s *session) stop(timeout time.Duration) (alreadyStopped bool) {
	first := false
	s.stopOnce.Do(func() {
		first = true
		defer close(s.stoppedCh)
		if s.servers != nil {
			if err := s.servers.Shutdown(timeout); err != nil {
				Log().Error("Error shutting down servers", "session", s.id, "error", err)
			}
		}
		if s.mode == "record" && s.writable != nil {
			Log().Info("Writing archive", "path", s.archivePath)
			// Close serializes the archive, fsyncs it to disk, and closes
			// the file.
			var flushErr error
			if err := s.writable.Close(); err != nil {
				Log().Error("Error flushing archive", "error", err)
				flushErr = err
			}
			var data []byte
			if flushErr == nil {
				var readErr error
				data, readErr = os.ReadFile(s.archivePath)
				if readErr != nil {
					Log().Error("Error reading archive after close", "error", readErr)
					flushErr = readErr
				}
			}
			s.mu.Lock()
			s.archiveBytes = data
			if flushErr != nil {
				s.flushError = flushErr.Error()
			}
			s.mu.Unlock()
			if s.tempArchive && !s.keepTempFile {
				if err := os.Remove(s.archivePath); err != nil && !os.IsNotExist(err) {
					Log().Error("Error removing temp archive", "error", err)
				}
				s.mu.Lock()
				s.archivePath = ""
				s.mu.Unlock()
			}
		}
	})
	// Wait until the stop sequence has fully completed.
	<-s.stoppedCh
	return !first
}

// ---------------------------------------------------------------------------
// getArchive.
// ---------------------------------------------------------------------------

func handleGetArchive(requestJSON []byte) ([]byte, *wprError) {
	var req getArchiveRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return nil, newErr(codeInvalidConfig, "malformed getArchive request JSON: %v", err)
	}
	s, ok := lookupSession(req.Session)
	if !ok {
		return nil, newErr(codeSessionNotFound, "no session with id %d", req.Session)
	}
	if s.mode != "record" {
		return nil, newErr(codeUnsupportedMode,
			"getArchive is only valid for record sessions; session %d is %s mode",
			req.Session, s.mode)
	}
	s.mu.Lock()
	select {
	case <-s.stoppedCh:
	default:
		s.mu.Unlock()
		return nil, newErr(codeNotReady,
			"archive is not available until the session is stopped; call stop first")
	}
	archiveBytes := s.archiveBytes
	s.mu.Unlock()

	resp := getArchiveResponse{
		OK:            true,
		Session:       req.Session,
		ArchiveBase64: base64.StdEncoding.EncodeToString(archiveBytes),
		ArchiveLength: len(archiveBytes),
	}
	b, jsonErr := json.Marshal(resp)
	if jsonErr != nil {
		return nil, newErr(codeInternalError, "error encoding getArchive response: %v", jsonErr)
	}
	return b, nil
}

// handleDestroy removes a stopped session from the registry, releasing its
// retained archive bytes. Destroying an active (non-stopped) session is an
// error; use stop first.
func handleDestroy(requestJSON []byte) ([]byte, *wprError) {
	var req destroyRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return nil, newErr(codeInvalidConfig, "malformed destroy request JSON: %v", err)
	}
	s, ok := lookupSession(req.Session)
	if !ok {
		return nil, newErr(codeSessionNotFound, "no session with id %d", req.Session)
	}
	<-s.stoppedCh // block until any in-flight stop has completed
	regMu.Lock()
	delete(sessionsReg, req.Session)
	regMu.Unlock()
	resp := destroyResponse{OK: true, Session: req.Session}
	b, jsonErr := json.Marshal(resp)
	if jsonErr != nil {
		return nil, newErr(codeInternalError, "error encoding destroy response: %v", jsonErr)
	}
	return b, nil
}

// portBindDetails extracts structured info from a StartServers bind failure
// for the error response's "details" field.
func portBindDetails(err error) map[string]any {
	details := map[string]any{}
	var bindErr *webpagereplay.BindError
	if errors.As(err, &bindErr) {
		details["scheme"] = bindErr.Scheme
		details["port"] = bindErr.Port
		details["host"] = bindErr.Host
	}
	return details
}
