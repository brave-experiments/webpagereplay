// Copyright 2017 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("not found")

// ArchivedWebSocketMessage is a single message recorded on a WebSocket
// connection.
type ArchivedWebSocketMessage struct {
	// TimestampMs is the time, in milliseconds, between the completion of the
	// WebSocket handshake and this message. It is recorded but not used for
	// replay (messages are served in order, as soon as possible).
	TimestampMs int64 `json:"timestamp_ms"`
	// Direction is WsServerToClient or WsClientToServer.
	Direction int `json:"direction"`
	// Opcode is the WebSocket opcode of the message
	// (e.g. 0x1 text, 0x2 binary, 0x8 close, 0x9 ping, 0xA pong).
	Opcode int `json:"opcode"`
	// Payload is the unmasked message payload. For close messages it contains
	// the status code.
	Payload []byte `json:"payload,omitempty"`
}

// ArchivedRequest contains a single request and its response.
// The request/response fields are immutable after creation.
type ArchivedRequest struct {
	SerializedRequest   []byte
	SerializedResponse  []byte // if empty, the request failed
	LastServedSessionId uint32
	// WebSocketMessages contains the messages relayed on a WebSocket
	// connection established by this request, in chronological order.
	// It is non-nil if and only if this entry is a recorded WebSocket
	// handshake.
	WebSocketMessages []ArchivedWebSocketMessage
}

// RequestMatch represents a match when querying the archive for responses to a request
type RequestMatch struct {
	Match      *ArchivedRequest
	Request    *http.Request
	Response   *http.Response
	MatchRatio float64
}

func (requestMatch *RequestMatch) SetMatch(
	match *ArchivedRequest,
	request *http.Request,
	response *http.Response,
	ratio float64) {
	requestMatch.Match = match
	requestMatch.Request = request
	requestMatch.Response = response
	requestMatch.MatchRatio = ratio
}

func serializeRequest(req *http.Request, resp *http.Response) (*ArchivedRequest, error) {
	ar := &ArchivedRequest{}
	{
		var buf bytes.Buffer
		if err := req.Write(&buf); err != nil {
			return nil, fmt.Errorf("failed writing request for %s: %v", req.URL.String(), err)
		}
		ar.SerializedRequest = buf.Bytes()
	}
	{
		var buf bytes.Buffer
		if err := resp.Write(&buf); err != nil {
			return nil, fmt.Errorf("failed writing response for %s: %v", req.URL.String(), err)
		}
		ar.SerializedResponse = buf.Bytes()
	}
	return ar, nil
}

func (ar *ArchivedRequest) unmarshal(scheme string) (*http.Request, *http.Response, error) {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(ar.SerializedRequest)))
	if err != nil {
		return nil, nil, fmt.Errorf("couldn't unmarshal request: %v", err)
	}

	if req.URL.Host == "" {
		req.URL.Host = req.Host
		req.URL.Scheme = scheme
	}

	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(ar.SerializedResponse)), req)
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, nil, fmt.Errorf("couldn't unmarshal response: %v", err)
	}
	return req, resp, nil
}

// Archive contains an archive of requests. Immutable except when embedded in
// a WritableArchive.
// Fields are exported to enabled JSON encoding.
// LINT.IfChange(archive_struct)
type Archive struct {
	// Requests maps host(url) => url => []request.
	// The two-level mapping makes it easier to search for similar requests.
	// There may be multiple requests for a given URL.
	Requests map[string]map[string][]*ArchivedRequest
	// Maps host string to DER encoded certs.
	Certs map[string][]byte
	// Maps host string to the negotiated protocol. eg. "http/1.1" or "h2"
	// If absent, will default to "http/1.1".
	// Note: the protocol could be inferred from `Requests`, so this field seems
	// redundant.
	NegotiatedProtocol map[string]string
	// The time seed that was used to initialize deterministic.js.
	DeterministicTimeSeedMs int64
	// The constant value that Math.random() returns, if specified and
	// deterministic.js is injected.
	ConstantMathRandomResult *float64
	// When an incoming request matches multiple recorded responses, whether to
	// serve the responses in the chronological sequence in which wpr_go
	// recorded them.
	ServeResponseInChronologicalSequence bool
	// Records the current session id.
	// Archive can serve responses in chronological order. If a client wants to
	// reset the Archive to serve responses from the start, the client may do so
	// by incrementing its session id.
	CurrentSessionId uint32
	// If an incoming URL doesn't exactly match an entry in the archive,
	// skip fuzzy matching and return nothing.
	DisableFuzzyURLMatching bool
	// Metadata contains arbitrary text about the archive.
	Metadata string
	// Scripts to inject in all pages. Map from name to contents.
	InjectedScripts map[string]string
}

// LINT.ThenChange(archive.go:archive_clone)

func newArchive() Archive {
	return Archive{
		Requests:        make(map[string]map[string][]*ArchivedRequest),
		InjectedScripts: make(map[string]string),
	}
}

func prepareArchiveForReplay(a *Archive) {
	// Initialize the session id mechanism that Archive uses to keep state
	// information about clients.
	a.CurrentSessionId = 1
}

// OpenArchive opens an archive file previously written by OpenWritableArchive.
func OpenArchive(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %v", path, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gunzip failed: %v", err)
	}
	defer gz.Close()
	buf, err := ioutil.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("read failed: %v", err)
	}
	a := newArchive()
	if err := json.Unmarshal(buf, &a); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %v", err)
	}
	prepareArchiveForReplay(&a)
	return &a, nil
}

// ForEach applies f to all requests in the archive. The ArchivedRequest is
// passed as well so that callers can access archive-specific metadata such as
// recorded WebSocket messages.
// Although `req` and `resp` are pointers, mutating them won't actually change
// the contents of the archive. If you need to mutate, create a new archive and
// add to it.
func (a *Archive) ForEach(f func(ar *ArchivedRequest, req *http.Request, resp *http.Response) error) error {
	for _, urlmap := range a.Requests {
		for urlString, requests := range urlmap {
			fullURL, _ := url.Parse(urlString)
			for index, archivedRequest := range requests {
				req, resp, err := archivedRequest.unmarshal(fullURL.Scheme)
				if err != nil {
					Log().Error("Error unmarshaling request", "index", index,
						"url", urlString, "error", err)
					continue
				}
				if err := f(archivedRequest, req, resp); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Returns the der encoded cert.
func (a *Archive) FindHostCertificate(host string) ([]byte, error) {
	if cert, ok := a.Certs[host]; ok {
		return cert, nil
	}
	return nil, ErrNotFound
}

func (a *Archive) FindHostNegotiatedProtocol(host string) (string, error) {
	if negotiatedProtocol, ok := a.NegotiatedProtocol[host]; ok {
		return negotiatedProtocol, nil
	}
	return "", ErrNotFound
}

func assertCompleteURL(url *url.URL) {
	if url.Host == "" || url.Scheme == "" {
		// TODO: Handle this more gracefully.
		Log().Error("Missing host and scheme", "url", url)
		os.Exit(1)
	}
}

// Returns a new archive with all fields cloned, apart from requests.
// LINT.IfChange(archive_clone)
func (a *Archive) cloneFieldsExceptRequests() Archive {
	// Clone elements that do NOT require a deep-copy. (Other than Requests.)
	clone := Archive{
		Requests:                             make(map[string]map[string][]*ArchivedRequest),
		DeterministicTimeSeedMs:              a.DeterministicTimeSeedMs,
		ServeResponseInChronologicalSequence: a.ServeResponseInChronologicalSequence,
		CurrentSessionId:                     a.CurrentSessionId,
		DisableFuzzyURLMatching:              a.DisableFuzzyURLMatching,
		Metadata:                             a.Metadata,
	}

	// Clone elements that DO require a deep-copy.
	if a.Certs != nil {
		clone.Certs = make(map[string][]byte, len(a.Certs))
		for k, v := range a.Certs {
			if v != nil {
				clone.Certs[k] = make([]byte, len(v))
				copy(clone.Certs[k], v)
			} else {
				clone.Certs[k] = nil
			}
		}
	}
	if a.NegotiatedProtocol != nil {
		clone.NegotiatedProtocol = make(map[string]string, len(a.NegotiatedProtocol))
		for k, v := range a.NegotiatedProtocol {
			clone.NegotiatedProtocol[k] = v
		}
	}
	if a.ConstantMathRandomResult != nil {
		val := *a.ConstantMathRandomResult
		clone.ConstantMathRandomResult = &val
	}
	if a.InjectedScripts != nil {
		clone.InjectedScripts = make(map[string]string, len(a.InjectedScripts))
		for k, v := range a.InjectedScripts {
			clone.InjectedScripts[k] = v
		}
	}

	return clone
}

// LINT.ThenChange(archive.go:archive_struct)

// FindRequest searches for the given request in the archive.
// Returns ErrNotFound if the request could not be found.
func (a *Archive) FindRequest(req *http.Request) (*http.Request, *http.Response, error) {
	_, req, resp, err := a.FindArchivedRequest(req)
	return req, resp, err
}

// FindArchivedRequest is FindRequest, but also returns the underlying
// ArchivedRequest, which carries archive-specific metadata such as recorded
// WebSocket messages.
func (a *Archive) FindArchivedRequest(req *http.Request) (*ArchivedRequest, *http.Request, *http.Response, error) {
	// Clear the input channel on large uploads to prevent WPR
	// from resetting the connection, and causing the upload
	// to fail.
	// Large upload is an uncommon scenario for WPR users. To
	// avoid exacting an overhead on every request, restrict
	// the operation to large uploads only (size > 1MB).
	if req.Body != nil &&
		(strings.EqualFold("POST", req.Method) || strings.EqualFold("PUT", req.Method)) &&
		req.ContentLength > 2<<20 {
		buf := make([]byte, 1024)
		for {
			_, readErr := req.Body.Read(buf)
			if readErr == io.EOF {
				break
			}
		}
	}

	hostMap := a.Requests[req.Host]
	if len(hostMap) == 0 {
		return nil, nil, nil, ErrNotFound
	}

	// Exact match. Note that req may be relative, but hostMap keys are always absolute.
	assertCompleteURL(req.URL)
	reqUrl := req.URL.String()

	if len(hostMap[reqUrl]) > 0 {
		return a.findBestMatchInArchivedRequestSet(req, hostMap[reqUrl])
	}

	// For all URLs with a matching path, pick the URL that has the most matching query parameters.
	// The match ratio is defined to be 2*M/T, where
	//   M = number of matches x where a.Query[x]=b.Query[x]
	//   T = sum(len(a.Query)) + sum(len(b.Query))
	aq := req.URL.Query()

	var bestURL string
	var bestURLs []string // For debugging fuzzy matching
	var bestRatio float64

	for ustr := range hostMap {
		u, err := url.Parse(ustr)
		if err != nil {
			continue
		}
		if u.Path != req.URL.Path {
			continue
		}
		bq := u.Query()
		m := 1
		t := len(aq) + len(bq)
		for k, v := range aq {
			if reflect.DeepEqual(v, bq[k]) {
				m++
			}
		}
		ratio := 2 * float64(m) / float64(t)

		if ratio > bestRatio {
			bestURLs = nil
		}
		bestURLs = append(bestURLs, bestURL)

		if ratio > bestRatio ||
			// Map iteration order is non-deterministic, so we must break ties.
			(ratio == bestRatio && ustr < bestURL) {
			bestURL = ustr
			bestRatio = ratio
		}
	}

	if bestURL != "" && !a.DisableFuzzyURLMatching {
		return a.findBestMatchInArchivedRequestSet(req, hostMap[bestURL])
	}

	if a.DisableFuzzyURLMatching {
		Log().Debug("No exact match found", "url", reqUrl,
			"num_matches", len(bestURLs), "matches", strings.Join(bestURLs, ","))
	}

	return nil, nil, nil, ErrNotFound
}

// Given an incoming request and a set of matches in the archive, identify the best match,
// based on request headers.
// Returns a match if there is one, or ErrNotFound.
func (a *Archive) findBestMatchInArchivedRequestSet(
	incomingReq *http.Request,
	archivedReqs []*ArchivedRequest) (
	*ArchivedRequest, *http.Request, *http.Response, error) {
	scheme := incomingReq.URL.Scheme

	if len(archivedReqs) == 0 {
		return nil, nil, nil, ErrNotFound
	} else if len(archivedReqs) == 1 {
		archivedReq, archivedResp, err := archivedReqs[0].unmarshal(scheme)
		if err != nil {
			Log().Error("Error unmarshaling request", "error", err)
			return nil, nil, nil, err
		}
		return archivedReqs[0], archivedReq, archivedResp, err
	}

	// There can be multiple requests with the same URL string. If that's the
	// case, break the tie by the number of headers that match.
	var bestMatch RequestMatch
	var bestInSequenceMatch RequestMatch

	for _, r := range archivedReqs {
		archivedReq, archivedResp, err := r.unmarshal(scheme)
		if err != nil {
			Log().Error("Error unmarshaling request", "error", err)
			continue
		}

		// Skip this archived request if the request methods does not match that
		// of the incoming request.
		if archivedReq.Method != incomingReq.Method {
			continue
		}

		// Count the number of header matches
		numMatchingHeaders := 1
		numTotalHeaders := len(incomingReq.Header) + len(archivedReq.Header)
		for key, val := range archivedReq.Header {
			if reflect.DeepEqual(val, incomingReq.Header[key]) {
				numMatchingHeaders++
			}
		}
		// Note that since |m| starts from 1. The ratio will be more than 0
		// even if no header matches.
		ratio := 2 * float64(numMatchingHeaders) / float64(numTotalHeaders)

		if a.ServeResponseInChronologicalSequence &&
			r.LastServedSessionId != a.CurrentSessionId &&
			ratio > bestInSequenceMatch.MatchRatio {
			bestInSequenceMatch.SetMatch(r, archivedReq, archivedResp, ratio)
		}
		if ratio > bestMatch.MatchRatio {
			bestMatch.SetMatch(r, archivedReq, archivedResp, ratio)
		}
	}

	if a.ServeResponseInChronologicalSequence &&
		bestInSequenceMatch.Match != nil {
		bestInSequenceMatch.Match.LastServedSessionId = a.CurrentSessionId
		return bestInSequenceMatch.Match, bestInSequenceMatch.Request, bestInSequenceMatch.Response, nil
	} else if bestMatch.Match != nil {
		bestMatch.Match.LastServedSessionId = a.CurrentSessionId
		return bestMatch.Match, bestMatch.Request, bestMatch.Response, nil
	}

	return nil, nil, nil, ErrNotFound
}

type AddMode int

const (
	AddModeAppend            AddMode = 0
	AddModeOverwriteExisting AddMode = 1
	AddModeSkipExisting      AddMode = 2
)

func (a *Archive) AddArchivedRequest(req *http.Request, resp *http.Response, mode AddMode) error {
	// Always use the absolute URL in this mapping.
	assertCompleteURL(req.URL)
	archivedRequest, err := serializeRequest(req, resp)
	if err != nil {
		return err
	}

	if a.Requests[req.Host] == nil {
		a.Requests[req.Host] = make(map[string][]*ArchivedRequest)
	}

	urlStr := req.URL.String()
	requests := a.Requests[req.Host][urlStr]
	if mode == AddModeAppend {
		requests = append(requests, archivedRequest)
	} else if mode == AddModeOverwriteExisting {
		Log().Warn("Overwriting existing request")
		requests = []*ArchivedRequest{archivedRequest}
	} else if mode == AddModeSkipExisting {
		if requests != nil {
			Log().Warn("Skipping existing request", "url", urlStr)
			return nil
		}
		requests = append(requests, archivedRequest)
	}
	a.Requests[req.Host][urlStr] = requests
	return nil
}

// AddArchivedRequestEntry adds a pre-built ArchivedRequest (e.g. one carrying
// recorded WebSocket messages) without re-serializing it. The request is used
// only to determine the archive keys (host and URL).
func (a *Archive) AddArchivedRequestEntry(ar *ArchivedRequest, req *http.Request, mode AddMode) error {
	// Always use the absolute URL in this mapping.
	assertCompleteURL(req.URL)
	if a.Requests[req.Host] == nil {
		a.Requests[req.Host] = make(map[string][]*ArchivedRequest)
	}
	urlStr := req.URL.String()
	requests := a.Requests[req.Host][urlStr]
	if mode == AddModeAppend {
		requests = append(requests, ar)
	} else if mode == AddModeOverwriteExisting {
		Log().Warn("Overwriting existing request")
		requests = []*ArchivedRequest{ar}
	} else if mode == AddModeSkipExisting {
		if requests != nil {
			Log().Warn("Skipping existing request", "url", urlStr)
			return nil
		}
		requests = append(requests, ar)
	}
	a.Requests[req.Host][urlStr] = requests
	return nil
}

// Start a new replay session so that the archive serves responses from the start.
// If an archive contains multiple identical requests with different responses, the archive
// can serve the responses in chronological order. This function resets the archive serving
// order to the start.
func (a *Archive) StartNewReplaySession() {
	a.CurrentSessionId++
}

// Edit iterates over all requests in the archive. For each request, it calls f to
// edit the request. If f returns a nil pair, the request is deleted.
// The edited archive is returned, leaving the current archive is unchanged.
func (a *Archive) Edit(edit func(req *http.Request, resp *http.Response) (*http.Request, *http.Response, error)) (*Archive, error) {
	clone := a.cloneFieldsExceptRequests()
	err := a.ForEach(func(ar *ArchivedRequest, oldReq *http.Request, oldResp *http.Response) error {
		newReq, newResp, err := edit(oldReq, oldResp)
		if err != nil {
			return err
		}
		if newReq == nil || newResp == nil {
			if newReq != nil || newResp != nil {
				panic("programming error: newReq/newResp must both be nil or non-nil")
			}
			return nil
		}
		if len(ar.WebSocketMessages) > 0 {
			// Recorded WebSocket sessions are not edited; keep them as-is.
			return clone.AddArchivedRequestEntry(ar, oldReq, AddModeAppend)
		}
		// TODO: allow changing scheme or protocol?
		return clone.AddArchivedRequest(newReq, newResp, AddModeAppend)
	})
	if err != nil {
		return nil, err
	}
	return &clone, nil
}

// Merge adds all the request of the provided archive to the receiver.
func (a *Archive) Merge(other *Archive, keepDuplicates bool) error {
	var numAddedRequests = 0
	var numSkippedRequests = 0
	err := other.ForEach(func(ar *ArchivedRequest, req *http.Request, resp *http.Response) error {
		foundReq, _, notFoundErr := a.FindRequest(req)
		if keepDuplicates || notFoundErr == ErrNotFound ||
			req.URL.String() != foundReq.URL.String() ||
			!reflect.DeepEqual(req.Header, foundReq.Header) {
			if len(ar.WebSocketMessages) > 0 {
				// Keep WebSocket sessions intact.
				if err := a.AddArchivedRequestEntry(ar, req, AddModeAppend); err != nil {
					return err
				}
				numAddedRequests++
				return nil
			}
			if err := a.AddArchivedRequest(req, resp, AddModeAppend); err != nil {
				return err
			}
			numAddedRequests++
		} else {
			numSkippedRequests++
		}
		return nil
	})
	Log().Info("Merged requests", "added", numAddedRequests,
		"duplicates", numSkippedRequests)
	return err
}

// Trim iterates over all requests in the archive. For each request, it calls f
// to see if the request should be removed the archive.
// The trimmed archive is returned, leaving the current archive unchanged.
func (a *Archive) Trim(trimMatch func(req *http.Request, resp *http.Response) (bool, error)) (*Archive, error) {
	var numRemovedRequests = 0
	clone := a.cloneFieldsExceptRequests()
	err := a.ForEach(func(ar *ArchivedRequest, req *http.Request, resp *http.Response) error {
		trimReq, err := trimMatch(req, resp)
		if err != nil {
			return err
		}
		if trimReq {
			numRemovedRequests++
		} else if len(ar.WebSocketMessages) > 0 {
			// Keep WebSocket sessions intact.
			if err := clone.AddArchivedRequestEntry(ar, req, AddModeAppend); err != nil {
				return err
			}
		} else {
			if err := clone.AddArchivedRequest(req, resp, AddModeAppend); err != nil {
				return err
			}
		}
		return nil
	})
	Log().Info("Trimmed requests", "removed", numRemovedRequests)
	if err != nil {
		return nil, err
	}
	return &clone, nil
}

// Add the result of a get request to the receiver.
func (a *Archive) Add(method string, urlString string, mode AddMode) error {
	req, err := http.NewRequest(method, urlString, nil)
	if err != nil {
		return fmt.Errorf("Error creating request object: %v", err)
	}

	url, _ := url.Parse(urlString)
	// Print a warning for duplicate requests since the replay server will only
	// return the first found response.
	if mode == AddModeAppend || mode == AddModeSkipExisting {
		if foundReq, _, notFoundErr := a.FindRequest(req); notFoundErr != ErrNotFound {
			if foundReq.URL.String() == url.String() {
				if mode == AddModeSkipExisting {
					Log().Warn("Skipping existing request", "method", req.Method,
						"url", urlString)
					return nil
				}
				Log().Warn("Adding duplicate request", "method", req.Method,
					"url", urlString)
			}
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("Error fetching url: %v", err)
	}

	if err = a.AddArchivedRequest(req, resp, mode); err != nil {
		return err
	}

	Log().Info("Added request", "method", req.Method, "status", resp.Status,
		"url", urlString)
	return nil
}

// Serialize serializes this archive to the given writer.
func (a *Archive) Serialize(w io.Writer) error {
	gz := gzip.NewWriter(w)
	if err := json.NewEncoder(gz).Encode(a); err != nil {
		return fmt.Errorf("json marshal failed: %v", err)
	}
	return gz.Close()
}

// WriteableArchive wraps an Archive with writable methods for recording.
// The file is not flushed until Close is called. All methods are thread-safe.
type WritableArchive struct {
	Archive
	f  *os.File
	mu sync.Mutex
}

// OpenWritableArchive opens an archive file for writing.
// The output is gzipped JSON.
func OpenWritableArchive(path string) (*WritableArchive, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %v", path, err)
	}
	return &WritableArchive{Archive: newArchive(), f: f}, nil
}

// RecordRequest records a request/response pair in the archive.
func (a *WritableArchive) RecordRequest(req *http.Request, resp *http.Response) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.AddArchivedRequest(req, resp, AddModeAppend)
}

// RecordWebSocketHandshake records a WebSocket handshake request and its 101
// response, and returns the archived request so that the messages recorded
// during the session can be attached to it later via SetWebSocketMessages.
func (a *WritableArchive) RecordWebSocketHandshake(req *http.Request, resp *http.Response) (*ArchivedRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	assertCompleteURL(req.URL)
	ar, err := serializeRequest(req, resp)
	if err != nil {
		return nil, err
	}
	ar.WebSocketMessages = []ArchivedWebSocketMessage{}
	if a.Requests[req.Host] == nil {
		a.Requests[req.Host] = make(map[string][]*ArchivedRequest)
	}
	urlStr := req.URL.String()
	a.Requests[req.Host][urlStr] = append(a.Requests[req.Host][urlStr], ar)
	return ar, nil
}

// SetWebSocketMessages attaches the messages recorded during a WebSocket
// session to the handshake previously recorded by RecordWebSocketHandshake.
func (a *WritableArchive) SetWebSocketMessages(ar *ArchivedRequest, msgs []ArchivedWebSocketMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ar.WebSocketMessages = msgs
}

// Must only be called if FindHostCertificate() returned ErrNotFound.
func (a *WritableArchive) RecordHostCertificate(host string, derBytes []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Certs == nil {
		a.Certs = make(map[string][]byte)
	}
	prevDerBytes, ok := a.Certs[host]
	if ok {
		if !areEquivalentCertChains(prevDerBytes, derBytes) {
			panic("must not record a host certificate when there's an existing one")
		}
		return
	}
	a.Certs[host] = derBytes
}

// Check whether two certificate chains are similar enough that we can consider
// them as equivalent.
//
// Note that this code is quite relaxed about security, as this is never
// expected to be used in contexts where security matters.
func areEquivalentCertChains(der1, der2 []byte) bool {
	if bytes.Equal(der1, der2) {
		return true
	}

	certs1, err1 := x509.ParseCertificates(der1)
	if err1 != nil {
		return false
	}

	certs2, err2 := x509.ParseCertificates(der2)
	if err2 != nil {
		return false
	}

	if len(certs1) != len(certs2) {
		return false
	}

	for i := range certs1 {
		c1, c2 := certs1[i], certs2[i]
		// We intentionally skip such fields as `SerialNumber`.
		if c1.SignatureAlgorithm != c2.SignatureAlgorithm ||
			c1.Issuer.String() != c2.Issuer.String() ||
			c1.Subject.String() != c2.Subject.String() ||
			!c1.NotBefore.Equal(c2.NotBefore) ||
			!c1.NotAfter.Equal(c2.NotAfter) ||
			!reflect.DeepEqual(c1.PublicKey, c2.PublicKey) ||
			!reflect.DeepEqual(c1.DNSNames, c2.DNSNames) ||
			!reflect.DeepEqual(c1.IPAddresses, c2.IPAddresses) ||
			!reflect.DeepEqual(c1.ExtKeyUsage, c2.ExtKeyUsage) ||
			c1.KeyUsage != c2.KeyUsage {
			return false
		}
	}

	return true
}

// Must only be called if FindHostNegotiatedProtocol() returned ErrNotFound.
func (a *WritableArchive) RecordHostNegotiatedProtocol(host string, negotiatedProtocol string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.NegotiatedProtocol == nil {
		a.NegotiatedProtocol = make(map[string]string)
	}
	protocol, ok := a.NegotiatedProtocol[host]
	if ok && protocol != negotiatedProtocol {
		panic("Must not rewrite the protocol with a different one.")
	}
	a.NegotiatedProtocol[host] = negotiatedProtocol
}

// Close flushes the the archive and closes the output file.
//
// Close serializes the archive from the current in-memory state, fsyncs it to
// disk, and closes the file, so callers always receive a fully flushed file.
// It must be called exactly once; subsequent calls return an error.
func (a *WritableArchive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() { a.f = nil }()
	if a.f == nil {
		return errors.New("already closed")
	}

	if err := a.Serialize(a.f); err != nil {
		return err
	}
	// Ensure the serialized bytes are durable on disk before closing.
	if err := a.f.Sync(); err != nil {
		a.f.Close()
		return err
	}
	return a.f.Close()
}
