// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"context"
	"hash/fnv"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ShapingConfig holds CLI parameters for ultra-realistic traffic shaping.
type ShapingConfig struct {
	Preset            string
	MinInitialDelayMs int64
	MaxInitialDelayMs int64
	MinChunkSizeBytes int
	MaxChunkSizeBytes int
	MinChunkDelayMs   int64
	MaxChunkDelayMs   int64
}

// NetworkPresets provides pre-configured profiles for common network classes.
var NetworkPresets = map[string]ShapingConfig{
	"3g": {
		MinInitialDelayMs: 100, MaxInitialDelayMs: 250,
		MinChunkSizeBytes: 1460, MaxChunkSizeBytes: 4380,
		MinChunkDelayMs: 10, MaxChunkDelayMs: 30,
	},
	"4g": {
		MinInitialDelayMs: 30, MaxInitialDelayMs: 70,
		MinChunkSizeBytes: 4380, MaxChunkSizeBytes: 14600,
		MinChunkDelayMs: 2, MaxChunkDelayMs: 10,
	},
	"5g": {
		MinInitialDelayMs: 10, MaxInitialDelayMs: 25,
		MinChunkSizeBytes: 14600, MaxChunkSizeBytes: 65535,
		MinChunkDelayMs: 1, MaxChunkDelayMs: 3,
	},
	"dsl": {
		MinInitialDelayMs: 20, MaxInitialDelayMs: 50,
		MinChunkSizeBytes: 1460, MaxChunkSizeBytes: 8760,
		MinChunkDelayMs: 5, MaxChunkDelayMs: 15,
	},
	"cable": {
		MinInitialDelayMs: 15, MaxInitialDelayMs: 30,
		MinChunkSizeBytes: 8760, MaxChunkSizeBytes: 32768,
		MinChunkDelayMs: 1, MaxChunkDelayMs: 5,
	},
	"satellite": {
		MinInitialDelayMs: 500, MaxInitialDelayMs: 700,
		MinChunkSizeBytes: 14600, MaxChunkSizeBytes: 32768,
		MinChunkDelayMs: 20, MaxChunkDelayMs: 50,
	},
}

// ApplyPreset fills empty values from NetworkPresets if a preset is named.
func (c *ShapingConfig) ApplyPreset() {
	if p, ok := NetworkPresets[strings.ToLower(c.Preset)]; ok {
		if c.MinInitialDelayMs == 0 {
			c.MinInitialDelayMs = p.MinInitialDelayMs
		}
		if c.MaxInitialDelayMs == 0 {
			c.MaxInitialDelayMs = p.MaxInitialDelayMs
		}
		if c.MinChunkSizeBytes == 0 {
			c.MinChunkSizeBytes = p.MinChunkSizeBytes
		}
		if c.MaxChunkSizeBytes == 0 {
			c.MaxChunkSizeBytes = p.MaxChunkSizeBytes
		}
		if c.MinChunkDelayMs == 0 {
			c.MinChunkDelayMs = p.MinChunkDelayMs
		}
		if c.MaxChunkDelayMs == 0 {
			c.MaxChunkDelayMs = p.MaxChunkDelayMs
		}
	}
}

// Enabled returns true if traffic shaping is configured.
func (c *ShapingConfig) Enabled() bool {
	return c.Preset != "" || c.MaxInitialDelayMs > 0 ||
		c.MaxChunkSizeBytes > 0 || c.MaxChunkDelayMs > 0
}

// ValidatePreset returns true if the preset name is recognized or empty.
func ValidatePreset(preset string) bool {
	if preset == "" {
		return true
	}
	_, ok := NetworkPresets[strings.ToLower(preset)]
	return ok
}

// Validate verifies that each traffic shaping flag value is valid and consistent.
func (c *ShapingConfig) Validate() bool {
	if !ValidatePreset(c.Preset) {
		return false
	}
	c.ApplyPreset()

	if c.MinInitialDelayMs < 0 || c.MaxInitialDelayMs < 0 {
		return false
	}
	if c.MaxInitialDelayMs > 0 && c.MinInitialDelayMs > c.MaxInitialDelayMs {
		return false
	}

	if c.MinChunkSizeBytes < 0 || c.MaxChunkSizeBytes < 0 {
		return false
	}
	if c.MaxChunkSizeBytes > 0 && c.MinChunkSizeBytes > c.MaxChunkSizeBytes {
		return false
	}

	if c.MinChunkDelayMs < 0 || c.MaxChunkDelayMs < 0 {
		return false
	}
	if c.MaxChunkDelayMs > 0 && c.MinChunkDelayMs > c.MaxChunkDelayMs {
		return false
	}

	return true
}

// CalculateSeed returns a stable int64 seed based on a string.
func CalculateSeed(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64())
}

// StreamState holds runtime metrics across a single stream lifecycle.
type StreamState struct {
	TotalBytesSent int64
	ChunksSent     int
	StartTime      time.Time
	Context        map[string]any
	Rand           *rand.Rand
	TargetDeadline time.Time
}

// ShaperStrategy defines the contract for traffic profile behaviors.
type ShaperStrategy interface {
	InitialDelay(seed int64) time.Duration
	NextChunk(state *StreamState) (chunkSize int, chunkDelay time.Duration)
}

// InitialDelay computes delay prior to transmitting headers/first byte.
func (c *ShapingConfig) InitialDelay(seed int64) time.Duration {
	delta := c.MaxInitialDelayMs - c.MinInitialDelayMs
	if delta <= 0 {
		return time.Duration(c.MinInitialDelayMs) * time.Millisecond
	}
	r := rand.New(rand.NewSource(seed))
	ran := r.Int63n(delta)
	return time.Duration(c.MinInitialDelayMs+ran) * time.Millisecond
}

// NextChunk determines the size and delay of the next transmission chunk.
func (c *ShapingConfig) NextChunk(state *StreamState) (int, time.Duration) {
	chunkSize := c.MinChunkSizeBytes
	sizeDelta := c.MaxChunkSizeBytes - c.MinChunkSizeBytes
	if sizeDelta > 0 {
		chunkSize += state.Rand.Intn(sizeDelta + 1)
	}

	chunkDelayMs := c.MinChunkDelayMs
	delayDelta := c.MaxChunkDelayMs - c.MinChunkDelayMs
	if delayDelta > 0 {
		chunkDelayMs += state.Rand.Int63n(delayDelta)
	}

	dur := time.Duration(chunkDelayMs) * time.Millisecond
	return chunkSize, dur
}

var streamBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

// StreamShapedResponse handles hyper-efficient streaming of shaped responses
// using buffer pooling and absolute target pacing.
func StreamShapedResponse(
	ctx context.Context,
	w http.ResponseWriter,
	r io.Reader,
	strategy ShaperStrategy,
	seed int64,
) (int64, error) {
	flusher, canFlush := w.(http.Flusher)

	now := time.Now()
	state := &StreamState{
		StartTime:      now,
		Context:        make(map[string]any),
		Rand:           rand.New(rand.NewSource(seed)),
		TargetDeadline: now,
	}

	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	buf := *bufPtr

	chunkTimer := time.NewTimer(0)
	if !chunkTimer.Stop() {
		select {
		case <-chunkTimer.C:
		default:
		}
	}

	for {
		if ctx.Err() != nil {
			return state.TotalBytesSent, ctx.Err()
		}

		chunkSize, chunkDelay := strategy.NextChunk(state)
		if chunkSize <= 0 {
			chunkSize = 1024
		}
		if chunkSize > len(buf) {
			chunkSize = len(buf)
		}

		n, readErr := r.Read(buf[:chunkSize])
		if n > 0 {
			if chunkDelay > 0 {
				state.TargetDeadline = state.TargetDeadline.Add(chunkDelay)
				delay := time.Until(state.TargetDeadline)
				if delay > 0 {
					chunkTimer.Reset(delay)
					select {
					case <-chunkTimer.C:
					case <-ctx.Done():
						chunkTimer.Stop()
						return state.TotalBytesSent, ctx.Err()
					}
				}
			}

			wBytes, writeErr := w.Write(buf[:n])
			state.TotalBytesSent += int64(wBytes)
			state.ChunksSent++

			if canFlush {
				flusher.Flush()
			}

			if writeErr != nil {
				return state.TotalBytesSent, writeErr
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return state.TotalBytesSent, readErr
		}
	}

	return state.TotalBytesSent, nil
}
