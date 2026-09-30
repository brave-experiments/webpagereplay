// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// ServerConfig describes the set of servers to start.
//
// A port value of -1 disables the corresponding server, 0 makes the kernel
// pick an available port, and a value > 0 binds to that fixed port.
type ServerConfig struct {
	Host                string // bind address, e.g. "localhost"
	HTTPPort            int    // -1 disabled, 0 auto-select, >0 fixed
	HTTPSPort           int    // -1 disabled, 0 auto-select, >0 fixed
	HTTPSecureProxyPort int    // -1 disabled, 0 auto-select, >0 fixed
	TLSConfig           *tls.Config

	// HTTPHandler serves plain HTTP and, if HTTPSecureProxyPort is enabled,
	// also serves HTTP-over-HTTPS proxy requests.
	HTTPHandler http.Handler
	// HTTPSHandler serves HTTPS (MITM) traffic.
	HTTPSHandler http.Handler
}

// BindError describes a failure to bind a listener, with structured context.
type BindError struct {
	Scheme string
	Host   string
	Port   int
	Err    error
}

func (e *BindError) Error() string {
	return fmt.Sprintf("failed to bind %s server on port %d (host %q): %v", e.Scheme, e.Port, e.Host, e.Err)
}

func (e *BindError) Unwrap() error { return e.Err }

// ListenerInfo describes one bound listener.
type ListenerInfo struct {
	Scheme string
	Addr   net.Addr
}

// ServerSet holds the listeners and servers started by StartServers, together
// with the actual bound ports and a way to shut everything down gracefully.
type ServerSet struct {
	httpPort            int // actual bound port, -1 if disabled
	httpsPort           int
	httpSecureProxyPort int

	listeners []ListenerInfo
	servers   []*startedServer

	serveWG      sync.WaitGroup
	shutdownOnce sync.Once
	mu           sync.Mutex
	stopped      bool
}

type startedServer struct {
	scheme string
	srv    *http.Server
	ln     net.Listener
}

// ActualHTTPPort returns the port the HTTP server bound to, or -1 if disabled.
func (ss *ServerSet) ActualHTTPPort() int { return ss.httpPort }

// ActualHTTPSPort returns the port the HTTPS server bound to, or -1 if disabled.
func (ss *ServerSet) ActualHTTPSPort() int { return ss.httpsPort }

// ActualHTTPSecureProxyPort returns the port the HTTPS-to-HTTP secure proxy
// server bound to, or -1 if disabled.
func (ss *ServerSet) ActualHTTPSecureProxyPort() int { return ss.httpSecureProxyPort }

// Listeners returns the bound listeners in the order: http, https,
// https-to-http secure proxy. Disabled servers are omitted.
func (ss *ServerSet) Listeners() []ListenerInfo {
	return append([]ListenerInfo(nil), ss.listeners...)
}

// StartServers binds all requested listeners synchronously and starts serving
// them in background goroutines. If any bind fails, all already-bound
// listeners are closed and a descriptive error naming the scheme, requested
// port, and underlying OS error is returned. On success, the actual bound
// ports (important when a port of 0 was requested) are exposed through the
// returned ServerSet.
func StartServers(cfg ServerConfig) (*ServerSet, error) {
	ss := &ServerSet{
		httpPort:            -1,
		httpsPort:           -1,
		httpSecureProxyPort: -1,
	}

	// Phase 1: bind all requested listeners synchronously so that failures
	// can be reported before any serving starts.
	type request struct {
		scheme  string
		port    int
		secure  bool // TLS-wrap the listener
		handler http.Handler
		tlsSrv  bool // set http.Server.TLSConfig and configure HTTP/2
	}
	requests := []request{}
	if cfg.HTTPPort > -1 {
		requests = append(requests, request{
			scheme: "http", port: cfg.HTTPPort, handler: cfg.HTTPHandler,
		})
	}
	if cfg.HTTPSPort > -1 {
		requests = append(requests, request{
			scheme: "https", port: cfg.HTTPSPort, secure: true, tlsSrv: true,
			handler: cfg.HTTPSHandler,
		})
	}
	if cfg.HTTPSecureProxyPort > -1 {
		requests = append(requests, request{
			scheme: "https", port: cfg.HTTPSecureProxyPort, secure: true,
			handler: cfg.HTTPHandler, // this server proxies HTTP requests over an HTTPS connection
		})
		// Note: for this server the http.Server deliberately keeps a nil
		// TLSConfig (it is a proxy, not a MITM server); only its listener is
		// TLS-wrapped with the shared config.
	}

	var bound []*startedServer
	for _, r := range requests {
		ln, err := getListener(cfg.Host, r.port)
		if err != nil {
			// Close the already-bound listeners before failing.
			for _, b := range bound {
				b.ln.Close()
			}
			return nil, &BindError{Scheme: r.scheme, Host: cfg.Host, Port: r.port, Err: err}
		}
		srv := &http.Server{
			Addr:    fmt.Sprintf("%v:%v", cfg.Host, r.port),
			Handler: r.handler,
		}
		if r.tlsSrv {
			srv.TLSConfig = cfg.TLSConfig
		}
		if r.secure {
			// The original CLI configured HTTP/2 on every TLS-wrapped server.
			http2.ConfigureServer(srv, &http2.Server{})
		}
		listener := net.Listener(tcpKeepAliveListener{ln.(*net.TCPListener)})
		if r.secure {
			// TLS-wrap the keep-alive listener, mirroring the historical
			// behavior in wpr.go.
			listener = tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, cfg.TLSConfig)
		}
		bound = append(bound, &startedServer{scheme: r.scheme, srv: srv, ln: listener})
		switch r.scheme {
		case "http":
			ss.httpPort = ln.Addr().(*net.TCPAddr).Port
		case "https":
			if r.tlsSrv {
				ss.httpsPort = ln.Addr().(*net.TCPAddr).Port
			} else {
				ss.httpSecureProxyPort = ln.Addr().(*net.TCPAddr).Port
			}
		}
		ss.listeners = append(ss.listeners, ListenerInfo{Scheme: r.scheme, Addr: ln.Addr()})
	}

	// Phase 2: serve on all bound listeners in background goroutines.
	for i := range bound {
		b := bound[i]
		ss.serveWG.Add(1)
		go func() {
			defer ss.serveWG.Done()
			if err := b.srv.Serve(b.ln); err != nil && err != http.ErrServerClosed {
				Log().Error("Failed to start server", "scheme", b.scheme, "addr", b.srv.Addr, "error", err)
			}
		}()
	}
	ss.servers = bound
	return ss, nil
}

// Shutdown gracefully shuts down every server, waiting up to |timeout| for
// in-flight requests to finish. Servers that do not drain in time are closed
// forcefully. Shutdown waits for the serve goroutines to exit and is
// idempotent: subsequent calls return nil.
func (ss *ServerSet) Shutdown(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var retErr error
	ss.shutdownOnce.Do(func() {
		for _, s := range ss.servers {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := s.srv.Shutdown(ctx)
			cancel()
			if err != nil {
				if retErr == nil {
					retErr = err
				}
				s.srv.Close()
			}
		}
		// Wait for the serve goroutines to exit; fall back to Close if they
		// do not drain in time.
		done := make(chan struct{})
		go func() {
			ss.serveWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(timeout):
			for _, s := range ss.servers {
				s.srv.Close()
			}
			<-done
		}
	})
	ss.mu.Lock()
	ss.stopped = true
	ss.mu.Unlock()
	return retErr
}

// Close is a convenience alias for Shutdown with a default timeout.
func (ss *ServerSet) Close() error {
	return ss.Shutdown(5 * time.Second)
}

// Stopped reports whether Shutdown has been called.
func (ss *ServerSet) Stopped() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.stopped
}

func getListener(host string, port int) (net.Listener, error) {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%v:%d", host, port))
	if err != nil {
		return nil, err
	}
	return net.ListenTCP("tcp", addr)
}

// Copied from https://golang.org/src/net/http/server.go.
// This is to make dead TCP connections to eventually go away.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}
