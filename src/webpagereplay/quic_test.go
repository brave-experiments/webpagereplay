// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestHTTP3Server_EndToEnd(t *testing.T) {
	// 1. Generate test dynamic root CA
	rootCert := generateTestRootCA(t)
	rootCertParsed, err := x509.ParseCertificate(rootCert.Certificate[0])
	if err != nil {
		t.Fatalf("Failed to parse root CA cert: %v", err)
	}

	// 2. Build mock WPR archive
	archive := &Archive{
		Requests:           make(map[string]map[string][]*ArchivedRequest),
		Certs:              make(map[string][]byte),
		NegotiatedProtocol: map[string]string{"example.com": "h3"},
	}

	// Create a valid mock request and response
	req, err := http.NewRequest("GET", "https://example.com/h3-test", nil)
	if err != nil {
		t.Fatalf("Failed to create mock request: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Proto:      "HTTP/3.0",
		ProtoMajor: 3,
		ProtoMinor: 0,
		Header: http.Header{
			"Content-Type": []string{"text/plain"},
		},
		Body:          io.NopCloser(strings.NewReader("Hello from HTTP/3 Replay!")),
		ContentLength: int64(len("Hello from HTTP/3 Replay!")),
		Request:       req,
	}

	if err := archive.AddArchivedRequest(req, resp, AddModeAppend); err != nil {
		t.Fatalf("Failed to add mock request to archive: %v", err)
	}

	// 3. Generate dynamic HTTP/3 TLS Config
	h3TlsConfig, err := ReplayHTTP3TLSConfig([]tls.Certificate{rootCert}, archive, true)
	if err != nil {
		t.Fatalf("ReplayHTTP3TLSConfig failed: %v", err)
	}

	// 4. Bind dynamic UDP listener
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to resolve loopback UDP address: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("Failed to bind UDP socket: %v", err)
	}
	defer conn.Close()

	port := conn.LocalAddr().(*net.UDPAddr).Port

	// 5. Instantiate and start HTTP/3 server
	proxy := NewReplayingProxy(archive, "https", false, "")
	server := &http3.Server{
		Addr:      fmt.Sprintf("127.0.0.1:%d", port),
		TLSConfig: h3TlsConfig,
		Handler:   proxy,
	}

	go func() {
		if err := server.Serve(conn); err != nil && err != http.ErrServerClosed {
			t.Logf("Server closed with error: %v", err)
		}
	}()
	defer server.Close()

	// 6. Set up client CA pool and client with custom SNI override
	pool := x509.NewCertPool()
	pool.AddCert(rootCertParsed)

	client := &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: "example.com",
			},
		},
		Timeout: 5 * time.Second,
	}

	// 7. Execute transaction and verify response!
	clientReq, err := http.NewRequest("GET", fmt.Sprintf("https://127.0.0.1:%d/h3-test", port), nil)
	if err != nil {
		t.Fatalf("Failed to create client request: %v", err)
	}
	clientReq.Host = "example.com"

	clientResp, err := client.Do(clientReq)
	if err != nil {
		t.Fatalf("HTTP/3 request failed: %v", err)
	}
	defer clientResp.Body.Close()

	bodyBytes, err := io.ReadAll(clientResp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	bodyStr := string(bodyBytes)
	if clientResp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Body: %s", clientResp.StatusCode, bodyStr)
	}

	expectedBody := "Hello from HTTP/3 Replay!"
	if bodyStr != expectedBody {
		t.Errorf("Expected body %q, got %q", expectedBody, bodyStr)
	}
}
