// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"context"
	"io"
	"math/rand"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func TestCreateShapingConfig_Presets(t *testing.T) {
	cfg, err := CreateShapingConfig(ShapingOptions{Preset: "3g"})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed for 3g preset: %v", err)
	}

	rnd := rand.New(rand.NewSource(123))
	delay := cfg.InitialDelay(rnd)
	if delay < 100*time.Millisecond {
		t.Errorf("InitialDelay lower bound failed for 3g: got %v", delay)
	}
}

func TestCreateShapingConfig_PresetOverride(t *testing.T) {
	cfg, err := CreateShapingConfig(ShapingOptions{
		Preset:             "3g",
		MinInitialDelayMs:  i64(500),
		MaxInitialDelayMs:  i64(700),
		MaxPacketSizeBytes: i64(10000),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig override failed: %v", err)
	}

	rnd := rand.New(rand.NewSource(123))
	delay := cfg.InitialDelay(rnd)
	if delay < 500*time.Millisecond {
		t.Errorf("InitialDelay override failed for 3g: got %v", delay)
	}
}

func TestCreateShapingConfig_Validate(t *testing.T) {
	if _, err := CreateShapingConfig(ShapingOptions{
		Preset: "invalid"}); err == nil {
		t.Errorf("Expected error for invalid preset")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinInitialDelayMs: i64(-10)}); err == nil {
		t.Errorf("Expected error for negative min initial delay")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MaxInitialDelayMs: i64(-10)}); err == nil {
		t.Errorf("Expected error for negative max initial delay")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinInitialDelayMs: i64(100), MaxInitialDelayMs: i64(50)}); err == nil {
		t.Errorf("Expected error for min > max initial delay")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinPacketSizeBytes: i64(-1)}); err == nil {
		t.Errorf("Expected error for negative min packet size")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MaxPacketSizeBytes: i64(-1)}); err == nil {
		t.Errorf("Expected error for negative max packet size")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinPacketSizeBytes: i64(100), MaxPacketSizeBytes: i64(50)}); err == nil {
		t.Errorf("Expected error for min > max packet size")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinPacketDelayMs: i64(-1)}); err == nil {
		t.Errorf("Expected error for negative min packet delay")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MaxPacketDelayMs: i64(-1)}); err == nil {
		t.Errorf("Expected error for negative max packet delay")
	}
	if _, err := CreateShapingConfig(ShapingOptions{
		MinPacketDelayMs: i64(10), MaxPacketDelayMs: i64(5)}); err == nil {
		t.Errorf("Expected error for min > max packet delay")
	}
	if cfg, err := CreateShapingConfig(ShapingOptions{
		Preset: "3g"}); err != nil || cfg == nil {
		t.Errorf("Expected success for valid preset config")
	}
}

func TestCreateShapingConfig_Enabled(t *testing.T) {
	if cfg, _ := CreateShapingConfig(ShapingOptions{}); cfg != nil {
		t.Errorf("Empty config should result in nil (not enabled)")
	}
	if cfg, _ := CreateShapingConfig(ShapingOptions{
		Preset: "4g"}); cfg == nil {
		t.Errorf("Preset config should be enabled")
	}
	if cfg, _ := CreateShapingConfig(ShapingOptions{
		MaxInitialDelayMs: i64(10)}); cfg == nil {
		t.Errorf("Manual config should be enabled")
	}
}

func TestCreateShapingConfig_InitialDelay(t *testing.T) {
	cfg, err := CreateShapingConfig(ShapingOptions{
		MinInitialDelayMs: i64(50), MaxInitialDelayMs: i64(100)})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}
	rnd := rand.New(rand.NewSource(123))
	delay := cfg.InitialDelay(rnd)

	if delay < 50*time.Millisecond || delay >= 100*time.Millisecond {
		t.Errorf("InitialDelay out of bounds [50, 100): got %v", delay)
	}
}

func TestCreateShapingConfig_NextPacket(t *testing.T) {
	cfg, err := CreateShapingConfig(ShapingOptions{
		MinPacketSizeBytes: i64(100), MaxPacketSizeBytes: i64(200),
		MinPacketDelayMs: i64(5), MaxPacketDelayMs: i64(15),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}

	state := &StreamState{
		StartTime:      time.Now(),
		Rand:           rand.New(rand.NewSource(123)),
		SizeGenerator:  cfg.SizeGenerator,
		DelayGenerator: cfg.DelayGenerator,
	}
	size, delay := cfg.NextPacket(state)

	if size < 100 || size > 200 {
		t.Errorf("Packet size out of bounds [100, 200]: got %v", size)
	}
	if delay < 5*time.Millisecond || delay >= 15*time.Millisecond {
		t.Errorf("Packet delay out of bounds [5, 15): got %v", delay)
	}
}

func TestStreamShapedResponse(t *testing.T) {
	str := "UltraRealisticWebTrafficShapingTestString12345"
	originalContent := strings.Repeat(str, 50)
	reader := strings.NewReader(originalContent)
	rec := httptest.NewRecorder()

	cfg, err := CreateShapingConfig(ShapingOptions{
		MinPacketSizeBytes: i64(50),
		MaxPacketSizeBytes: i64(200),
		MinPacketDelayMs:   i64(1),
		MaxPacketDelayMs:   i64(2),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}

	ctx := context.Background()
	rnd := rand.New(rand.NewSource(123))
	totalBytes, err := StreamShapedResponse(ctx, rec, reader, cfg, rnd)
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

func TestWrapShapedReadCloser(t *testing.T) {
	originalContent := "TestingUpstreamRequestBodyShaping1234567890"
	r := strings.NewReader(originalContent)
	rc := io.NopCloser(r)

	cfg, err := CreateShapingConfig(ShapingOptions{
		MinPacketSizeBytes: i64(5),
		MaxPacketSizeBytes: i64(15),
		MinPacketDelayMs:   i64(1),
		MaxPacketDelayMs:   i64(2),
	})
	if err != nil || cfg == nil {
		t.Fatalf("CreateShapingConfig failed: %v", err)
	}

	rnd := rand.New(rand.NewSource(123))
	shaped := WrapShapedReadCloser(context.Background(), rc, cfg, rnd)

	buf, err := io.ReadAll(shaped)
	if err != nil {
		t.Fatalf("io.ReadAll on shaped reader failed: %v", err)
	}
	if err := shaped.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if string(buf) != originalContent {
		t.Errorf("Content mismatch. Got %s, expected %s", string(buf), originalContent)
	}
}

func TestCreateShapingConfig_AllPresets(t *testing.T) {
	presets := []string{"3g", "4g", "5g", "dsl", "cable", "satellite"}
	for _, preset := range presets {
		cfg, err := CreateShapingConfig(ShapingOptions{Preset: preset})
		if err != nil || cfg == nil {
			t.Errorf("CreateShapingConfig failed for named preset %q: %v", preset, err)
			continue
		}
		if cfg.InitGenerator == nil || cfg.SizeGenerator == nil || cfg.DelayGenerator == nil {
			t.Errorf("Named preset %q resulted in incomplete generators", preset)
		}
	}
}
