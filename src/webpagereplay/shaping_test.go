// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShapingConfig_Presets(t *testing.T) {
	cfg := ShapingConfig{Preset: "3g"}
	cfg.ApplyPreset()

	if cfg.MinInitialDelayMs != 100 || cfg.MaxInitialDelayMs != 250 {
		t.Errorf("Preset 3g failed initial delay defaults: got %v/%v",
			cfg.MinInitialDelayMs, cfg.MaxInitialDelayMs)
	}
	if cfg.MinChunkSizeBytes != 1460 || cfg.MaxChunkSizeBytes != 4380 {
		t.Errorf("Preset 3g failed chunk size defaults: got %v/%v",
			cfg.MinChunkSizeBytes, cfg.MaxChunkSizeBytes)
	}
	if cfg.MinChunkDelayMs != 10 || cfg.MaxChunkDelayMs != 30 {
		t.Errorf("Preset 3g failed chunk delay defaults: got %v/%v",
			cfg.MinChunkDelayMs, cfg.MaxChunkDelayMs)
	}
}

func TestShapingConfig_Enabled(t *testing.T) {
	if (&ShapingConfig{}).Enabled() {
		t.Errorf("Empty config should not be enabled")
	}
	if !(&ShapingConfig{Preset: "4g"}).Enabled() {
		t.Errorf("Preset config should be enabled")
	}
	if !(&ShapingConfig{MaxInitialDelayMs: 10}).Enabled() {
		t.Errorf("Manual config should be enabled")
	}
}

func TestShapingConfig_InitialDelay(t *testing.T) {
	cfg := ShapingConfig{MinInitialDelayMs: 50, MaxInitialDelayMs: 100}
	delay := cfg.InitialDelay()

	if delay < 50*time.Millisecond || delay >= 100*time.Millisecond {
		t.Errorf("InitialDelay out of bounds [50, 100): got %v", delay)
	}
}

func TestShapingConfig_NextChunk(t *testing.T) {
	cfg := ShapingConfig{
		MinChunkSizeBytes: 100, MaxChunkSizeBytes: 200,
		MinChunkDelayMs: 5, MaxChunkDelayMs: 15,
	}

	state := &StreamState{StartTime: time.Now()}
	size, delay := cfg.NextChunk(state)

	if size < 100 || size > 200 {
		t.Errorf("Chunk size out of bounds [100, 200]: got %v", size)
	}
	if delay < 5*time.Millisecond || delay >= 15*time.Millisecond {
		t.Errorf("Chunk delay out of bounds [5, 15): got %v", delay)
	}
}

func TestStreamShapedResponse(t *testing.T) {
	str := "UltraRealisticWebTrafficShapingTestString12345"
	originalContent := strings.Repeat(str, 50)
	reader := strings.NewReader(originalContent)
	rec := httptest.NewRecorder()

	cfg := ShapingConfig{
		MinChunkSizeBytes: 50,
		MaxChunkSizeBytes: 200,
		MinChunkDelayMs:   1,
		MaxChunkDelayMs:   2,
	}

	ctx := context.Background()
	totalBytes, err := StreamShapedResponse(ctx, rec, reader, &cfg)
	if err != nil {
		t.Fatalf("StreamShapedResponse returned error: %v", err)
	}

	if totalBytes != int64(len(originalContent)) {
		t.Errorf("Expected to return %v total bytes, got %v",
			len(originalContent), totalBytes)
	}

	gotBody := rec.Body.String()
	if gotBody != originalContent {
		t.Errorf("Streamed response mismatch. Got len %v, expected %v",
			len(gotBody), len(originalContent))
	}
}
