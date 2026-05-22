// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"io"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"
)

type shapedFileServer struct {
	root       string
	shapingCfg *ShapingConfig
}

// NewShapedFileServer returns an http.Handler that serves local files with optional traffic shaping.
func NewShapedFileServer(root string, shapingCfg *ShapingConfig) http.Handler {
	return &shapedFileServer{root: root, shapingCfg: shapingCfg}
}

func (s *shapedFileServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	Log().Debug("FileServer: Handling request", "method", req.Method, "url", req.URL.String())

	if req.URL.Path == "/web-page-replay-command-exit" {
		Log().Info("Received /web-page-replay-command-exit")
		Log().Info("Shutting down")
		os.Exit(0)
		return
	}

	cleanedPath := path.Clean(req.URL.Path)
	fullPath := filepath.Join(s.root, filepath.FromSlash(cleanedPath))

	fileInfo, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		http.NotFound(w, req)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if fileInfo.IsDir() {
		idxPath := filepath.Join(fullPath, "index.html")
		fileInfo, err = os.Stat(idxPath)
		if os.IsNotExist(err) {
			idxPath = filepath.Join(fullPath, "index.htm")
			fileInfo, err = os.Stat(idxPath)
		}
		if os.IsNotExist(err) {
			http.NotFound(w, req)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fullPath = idxPath
	}

	if s.shapingCfg == nil {
		http.ServeFile(w, req, fullPath)
		return
	}

	file, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	ext := filepath.Ext(fullPath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))
	w.Header().Set("Last-Modified", fileInfo.ModTime().UTC().Format(http.TimeFormat))

	seed := CalculateSeed(req.URL.String())
	rnd := rand.New(rand.NewSource(seed))

	initDelay := s.shapingCfg.InitialDelay(rnd)
	if initDelay > 0 {
		time.Sleep(initDelay)
	}

	w.WriteHeader(http.StatusOK)
	ctx := req.Context()
	if _, err := StreamShapedResponse(ctx, w, file, s.shapingCfg, rnd); err != nil && err != io.EOF {
		Log().Error("Client response truncated during shaped stream", "error", err)
	}
}
