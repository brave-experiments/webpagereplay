// Copyright 2017 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	tmpdir    string
	nocleanup = flag.Bool("nocleanup", false, "If true, don't cleanup temp files on shutdown.")
)

func TestMain(m *testing.M) {
	flag.Parse()
	var err error
	tmpdir, err = ioutil.TempDir("", "webpagereplay_proxy_test")
	if err != nil {
		Log().Error("Cannot make tempdir", "error", err)
		os.Exit(1)
	}
	ret := m.Run()
	if !*nocleanup {
		os.RemoveAll(tmpdir)
	}
	os.Exit(ret)
}

type wprTestEnv struct {
	t               *testing.T
	archivePath     string
	originServer    *httptest.Server
	recordServer    *httptest.Server
	recordArchive   *WritableArchive
	recordTransport *http.Transport

	replayServer    *httptest.Server
	replayArchive   *Archive
	replayTransport *http.Transport
}

func newWprTestEnv(
	t *testing.T,
	archiveName string,
	originHandler http.HandlerFunc,
	transformers []ResponseTransformer,
) *wprTestEnv {
	archivePath := filepath.Join(tmpdir, archiveName)
	origin := httptest.NewServer(http.HandlerFunc(originHandler))

	recordArchive, err := OpenWritableArchive(archivePath)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	recordProxy := NewRecordingProxy(
		recordArchive, "http", transformers, "")
	recordServer := httptest.NewServer(recordProxy)
	recordTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(recordServer.URL)
		},
	}

	return &wprTestEnv{
		t:               t,
		archivePath:     archivePath,
		originServer:    origin,
		recordServer:    recordServer,
		recordArchive:   recordArchive,
		recordTransport: recordTransport,
	}
}

func (c *wprTestEnv) RecordRequest(
	method string, path string, body string,
) (*http.Response, string) {
	var req *http.Request
	var err error
	u := c.originServer.URL + path
	if body != "" {
		req, err = http.NewRequest(method, u, strings.NewReader(body))
	} else {
		req, err = http.NewRequest(method, u, nil)
	}
	if err != nil {
		c.t.Fatalf("Record NewRequest(%s): %v", u, err)
	}
	resp, err := c.recordTransport.RoundTrip(req)
	if err != nil {
		c.t.Fatalf("Record RoundTrip(%s): %v", u, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("Record ReadAll(%s): %v", u, err)
	}
	return resp, string(b)
}

func (c *wprTestEnv) CloseRecord() {
	c.recordServer.Close()
	if err := c.recordArchive.Close(); err != nil {
		c.t.Fatalf("CloseArchive: %v", err)
	}
}

func (c *wprTestEnv) StartReplay(
	quietMode bool, paramToIgnore string, cfg *ShapingConfig,
) {
	replayArchive, err := OpenArchive(c.archivePath)
	if err != nil {
		c.t.Fatalf("OpenArchive: %v", err)
	}
	replayProxy := NewReplayingProxy(
		replayArchive, "http", quietMode, paramToIgnore, cfg)
	replayServer := httptest.NewServer(replayProxy)
	replayTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(replayServer.URL)
		},
	}
	c.replayArchive = replayArchive
	c.replayServer = replayServer
	c.replayTransport = replayTransport
}

func (c *wprTestEnv) ReplayRequest(
	method string, path string, body string,
) (*http.Response, string) {
	var req *http.Request
	var err error
	u := c.originServer.URL + path
	if body != "" {
		req, err = http.NewRequest(method, u, strings.NewReader(body))
	} else {
		req, err = http.NewRequest(method, u, nil)
	}
	if err != nil {
		c.t.Fatalf("Replay NewRequest(%s): %v", u, err)
	}
	resp, err := c.replayTransport.RoundTrip(req)
	if err != nil {
		c.t.Fatalf("Replay RoundTrip(%s): %v", u, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("Replay ReadAll(%s): %v", u, err)
	}
	return resp, string(b)
}

// Close terminates all remaining active origin and replaying HTTP servers.
func (c *wprTestEnv) Close() {
	c.originServer.Close()
	if c.replayServer != nil {
		c.replayServer.Close()
	}
}

// Tests that when --inject_scripts is provided during recording, the scripts
// are not saved as part of the response body (they are saved as separate
// special field in the archive instead).
func TestDoNotSaveInjectedScriptInResponseBody(t *testing.T) {
	originalBody := "<html><head></head><p>hello!</p></html>"
	si, err := NewScriptInjector([]byte("let x = 1;"), DefaultScriptInjectorConfig())
	if err != nil {
		t.Fatalf("failed to create script injector: %v", err)
	}

	c := newWprTestEnv(t, "TestDoNotSaveInjected.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, originalBody)
	}, []ResponseTransformer{si})
	defer c.Close()

	_, body := c.RecordRequest("GET", "/", "")
	fmt.Printf("body: %s", body)
	c.CloseRecord()

	replayArchive, err := OpenArchive(c.archivePath)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	u := c.originServer.URL + "/"
	req := httptest.NewRequest("GET", u, nil)
	_, recordedResp, err := replayArchive.FindRequest(req)
	if err != nil {
		t.Fatalf("unexpected error : %v", err)
	}
	defer recordedResp.Body.Close()
	recordedBody, err := io.ReadAll(recordedResp.Body)
	if err != nil {
		t.Fatalf("unexpected error : %v", err)
	}
	if got, want := string(recordedBody), originalBody; got != want {
		t.Errorf("response doesn't match:\n%q\n%q", got, want)
	}
	if string(recordedBody) == body {
		t.Fatal("served response body and recorded response body should not be equal")
	}
}

func TestEndToEnd(t *testing.T) {
	archiveFile := filepath.Join(tmpdir, "TestEndToEnd.json")

	// We will record responses from this server.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/img":
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Type", "image/webp")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "fake image body")
		case "/206":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "body")
		case "/post":
			w.Header().Set("Cache-Control", "private")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			io.Copy(w, req.Body)
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "default response")
		}
	}))
	defer origin.Close()

	// Start a proxy for the origin server that will construct an archive file.
	recordArchive, err := OpenWritableArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenWritableArchive: %v", err)
	}
	var transformers []ResponseTransformer
	recordServer := httptest.NewServer(NewRecordingProxy(recordArchive, "http", transformers, ""))
	recordTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(recordServer.URL)
		},
	}

	// Send a bunch of URLs to the server and record the responses.
	urls := []string{
		origin.URL + "/img",
		origin.URL + "/206",
		origin.URL + "/post",
	}
	type RecordedResponse struct {
		Code   int
		Header http.Header
		Body   string
	}
	recordResponse := func(u string, tr *http.Transport) (*RecordedResponse, error) {
		var req *http.Request
		var err error
		if strings.HasSuffix(u, "/post") {
			req, err = http.NewRequest("POST", u, strings.NewReader("this is the POST body"))
		} else {
			req, err = http.NewRequest("GET", u, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("NewRequest(%s): %v", u, err)
		}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			return nil, fmt.Errorf("RoundTrip(%s): %v", u, err)
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("ReadBody(%s): %v", u, err)
		}
		return &RecordedResponse{resp.StatusCode, resp.Header, string(body)}, nil
	}
	recorded := make(map[string]*RecordedResponse)
	for _, u := range urls {
		resp, err := recordResponse(u, recordTransport)
		if err != nil {
			t.Fatal(err)
		}
		recorded[u] = resp
	}

	// Shutdown and flush the archive.
	recordServer.Close()
	if err := recordArchive.Close(); err != nil {
		t.Fatalf("CloseArchive: %v", err)
	}
	recordArchive = nil
	recordServer = nil
	recordTransport = nil

	// Open a replay server using the saved archive.
	replayArchive, err := OpenArchive(archiveFile)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	replayServer := httptest.NewServer(NewReplayingProxy(replayArchive, "http", false, "", nil))
	replayTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(replayServer.URL)
		},
	}

	// Re-send the same URLs and ensure we get the same response.
	for _, u := range urls {
		resp, err := recordResponse(u, replayTransport)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := resp, recorded[u]; !reflect.DeepEqual(got, want) {
			t.Errorf("response doesn't match for %v:\n%+v\n%+v", u, got, want)
		}
	}
	// Check that a URL not found in the archive returns 404.
	resp, err := recordResponse(origin.URL+"/not_found_in_archive", replayTransport)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Code, http.StatusNotFound; got != want {
		t.Errorf("status code for /not_found_in_archive: got: %v want: %v", got, want)
	}
}

func TestUpdateDates(t *testing.T) {
	const (
		oldDate         = "Thu, 17 Aug 2017 12:00:00 GMT"
		oldLastModified = "Thu, 17 Aug 2017 09:00:00 GMT"
		oldExpires      = "Thu, 17 Aug 2017 17:00:00 GMT"
		newDate         = "Fri, 17 Aug 2018 12:00:00 GMT"
		newLastModified = "Fri, 17 Aug 2018 09:00:00 GMT"
		newExpires      = "Fri, 17 Aug 2018 17:00:00 GMT"
	)
	now, err := http.ParseTime(newDate)
	if err != nil {
		t.Fatal(err)
	}

	responseHeader := http.Header{
		"Date":          {oldDate},
		"Last-Modified": {oldLastModified},
		"Expires":       {oldExpires},
	}
	updateDates(responseHeader, now)

	wantHeader := http.Header{
		"Date":          {newDate},
		"Last-Modified": {newLastModified},
		"Expires":       {newExpires},
	}
	// Check if dates are updated as expected.
	if !reflect.DeepEqual(responseHeader, wantHeader) {
		t.Errorf("got: %v\nwant: %v\n", responseHeader, wantHeader)
	}
}

func TestProcessRequestURLParams(t *testing.T) {
	urlNormal := "https://example.com/path/to/resource?param1=value1&param2=value2"
	urlNoParam1 := "https://example.com/path/to/resource?param2=value2"
	urlMultpleParam1 := "https://example.com/path/to/resource?param1=value1&param2=value2&param1=value3"
	urlSequencePreserved := "https://example.com/path/to/resource?param1=value1&param2=value2&param3=value3&param1=value4&param2=value5"
	urlAnotherHost := "https://another_example.com/path/to/resource?param1=value1&param2=value2"

	validate := func(url string, paramToIgnoreInURLPath string, expectedURL string) error {
		req, err := http.NewRequest("POST", url, strings.NewReader("this is the POST body"))
		if err != nil {
			return fmt.Errorf("NewRequest(%s): %v", url, err)
		}
		if err := processRequestURLParams(req, paramToIgnoreInURLPath); err != nil {
			return fmt.Errorf("Error in processRequestURLParams: %v", err)
		}
		if req.URL.String() != expectedURL {
			t.Errorf("got processed URL: %v\nwant: %v\n", req.URL, expectedURL)
		}
		return nil
	}

	paramToIgnoreInURLPath := "https://example.com/path/to/resource::param1"
	tests := [][]string{
		{urlNormal, paramToIgnoreInURLPath, "https://example.com/path/to/resource?param2=value2"},
		{urlNoParam1, paramToIgnoreInURLPath, "https://example.com/path/to/resource?param2=value2"},
		{urlMultpleParam1, paramToIgnoreInURLPath, "https://example.com/path/to/resource?param2=value2"},
		{urlSequencePreserved, paramToIgnoreInURLPath, "https://example.com/path/to/resource?param2=value2&param3=value3&param2=value5"},
		{urlAnotherHost, paramToIgnoreInURLPath, "https://another_example.com/path/to/resource?param1=value1&param2=value2"},
	}

	for _, test := range tests {
		if err := validate(test[0], test[1], test[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplayingProxy_ShapingPOST(t *testing.T) {
	c := newWprTestEnv(t, "TestShapingPOST.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, req.Body)
	}, nil)
	defer c.Close()

	c.RecordRequest("POST", "/post", "Shaped POST upload test body")
	c.CloseRecord()

	cfg, err := CreateShapingConfig(ShapingOptions{
		Preset:            "3g",
		MinInitialDelayMs: i64(1),
		MaxInitialDelayMs: i64(2),
		MinPacketDelayMs:  i64(1),
		MaxPacketDelayMs:  i64(2),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}

	c.StartReplay(false, "", cfg)

	_, body := c.ReplayRequest("POST", "/post", "Shaped POST upload test body")
	if body != "Shaped POST upload test body" {
		t.Errorf("got %q want %q", body, "Shaped POST upload test body")
	}
}

func TestReplayingProxy_ShapingGET(t *testing.T) {
	targetBody := "Traffic shaped GET download content test body"
	c := newWprTestEnv(t, "TestShapingGET.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, targetBody)
	}, nil)
	defer c.Close()

	c.RecordRequest("GET", "/get", "")
	c.CloseRecord()

	cfg, err := CreateShapingConfig(ShapingOptions{
		Preset:            "4g",
		MinInitialDelayMs: i64(1),
		MaxInitialDelayMs: i64(2),
		MinPacketDelayMs:  i64(1),
		MaxPacketDelayMs:  i64(2),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}

	c.StartReplay(false, "", cfg)

	_, body := c.ReplayRequest("GET", "/get", "")
	if body != targetBody {
		t.Errorf("got %q want %q", body, targetBody)
	}
}
