// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ShapingConfig holds pre-initialized generators for zero-overhead streaming.
type ShapingConfig struct {
	InitGenerator  Generator
	SizeGenerator  Generator
	DelayGenerator Generator
}

// NetworkPresets provides pre-configured baseline values and profile parameters.
var NetworkPresets = map[string]ShapingConfig{
	"3g": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 100, Max: 250,
			JitterProb: 0.05, JitterSpike: 600,
			StallProb: 0.015, Stall: 1000,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 4380,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 80, Max: 120,
			JitterProb: 0.05, JitterSpike: 600,
			StallProb: 0.015, Stall: 1000,
		},
	},
	"4g": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 30, Max: 70,
			JitterProb: 0.05, JitterSpike: 250,
			StallProb: 0.005, Stall: 100,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 14600,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 35, Max: 50,
			JitterProb: 0.05, JitterSpike: 250,
			StallProb: 0.005, Stall: 100,
		},
	},
	"5g": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 10, Max: 25,
			JitterProb: 0.05, JitterSpike: 80,
			StallProb: 0.001, Stall: 20,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 65535,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 15, Max: 25,
			JitterProb: 0.05, JitterSpike: 80,
			StallProb: 0.001, Stall: 20,
		},
	},
	"dsl": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 20, Max: 50,
			JitterProb: 0.05, JitterSpike: 150,
			StallProb: 0.001, Stall: 50,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 8760,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 10, Max: 18,
			JitterProb: 0.05, JitterSpike: 150,
			StallProb: 0.001, Stall: 50,
		},
	},
	"cable": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 15, Max: 30,
			JitterProb: 0.05, JitterSpike: 150,
			StallProb: 0.001, Stall: 50,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 32768,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 10, Max: 18,
			JitterProb: 0.05, JitterSpike: 150,
			StallProb: 0.001, Stall: 50,
		},
	},
	"satellite": {
		InitGenerator: &HeavyTailedGenerator{
			Min: 500, Max: 700,
			JitterProb: 0.05, JitterSpike: 120,
			StallProb: 0.005, Stall: 100,
		},
		SizeGenerator: &BimodalGenerator{
			TinyProb: 0.45, MaxProb: 0.90,
			TinyMin: 40, TinyMax: 100,
			Mid: 101, Max: 32768,
		},
		DelayGenerator: &HeavyTailedGenerator{
			Min: 30, Max: 45,
			JitterProb: 0.05, JitterSpike: 120,
			StallProb: 0.005, Stall: 100,
		},
	},
}

// ValidatePreset returns true if the preset name is recognized or empty.
func ValidatePreset(preset string) bool {
	if preset == "" {
		return true
	}
	_, ok := NetworkPresets[strings.ToLower(preset)]
	return ok
}

// ShapingOptions defines configuration options passed from CLI flags.
type ShapingOptions struct {
	Preset             string
	MinInitialDelayMs  *int64
	MaxInitialDelayMs  *int64
	MinPacketSizeBytes *int
	MaxPacketSizeBytes *int
	MinPacketDelayMs   *int64
	MaxPacketDelayMs   *int64
}

func ptrInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func ptrInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// CreateShapingConfig validates CLI inputs and constructs the runtime profile.
func CreateShapingConfig(opts ShapingOptions) (*ShapingConfig, error) {
	if opts.Preset != "" {
		return createPresetShapingConfig(opts)
	}

	initMin := ptrInt64(opts.MinInitialDelayMs)
	initMax := ptrInt64(opts.MaxInitialDelayMs)
	sizeMin := ptrInt(opts.MinPacketSizeBytes)
	sizeMax := ptrInt(opts.MaxPacketSizeBytes)
	delayMin := ptrInt64(opts.MinPacketDelayMs)
	delayMax := ptrInt64(opts.MaxPacketDelayMs)

	linInit := &LinearGenerator{Min: initMin, Max: initMax}
	linSize := &LinearGenerator{Min: int64(sizeMin), Max: int64(sizeMax)}
	linDelay := &LinearGenerator{Min: delayMin, Max: delayMax}

	if err := linInit.Validate(); err != nil {
		return nil, fmt.Errorf("invalid initial delay bounds: %v", err)
	}
	if err := linSize.Validate(); err != nil {
		return nil, fmt.Errorf("invalid packet size bounds: %v", err)
	}
	if err := linDelay.Validate(); err != nil {
		return nil, fmt.Errorf("invalid packet delay bounds: %v", err)
	}

	if opts.MinInitialDelayMs == nil && opts.MaxInitialDelayMs == nil &&
		opts.MinPacketSizeBytes == nil && opts.MaxPacketSizeBytes == nil &&
		opts.MinPacketDelayMs == nil && opts.MaxPacketDelayMs == nil {
		return nil, nil
	}

	return &ShapingConfig{
		InitGenerator:  linInit,
		SizeGenerator:  linSize,
		DelayGenerator: linDelay,
	}, nil
}

func createPresetShapingConfig(opts ShapingOptions) (*ShapingConfig, error) {
	base, ok := NetworkPresets[strings.ToLower(opts.Preset)]
	if !ok {
		return nil, fmt.Errorf("invalid preset: %s", opts.Preset)
	}

	bSize := *base.SizeGenerator.(*BimodalGenerator)
	bDelay := *base.DelayGenerator.(*HeavyTailedGenerator)
	bInit := *base.InitGenerator.(*HeavyTailedGenerator)

	if opts.MaxPacketSizeBytes != nil {
		bSize.Max = int64(*opts.MaxPacketSizeBytes)
	}
	if opts.MinPacketDelayMs != nil {
		bDelay.Min = *opts.MinPacketDelayMs
	}
	if opts.MaxPacketDelayMs != nil {
		bDelay.Max = *opts.MaxPacketDelayMs
	}
	if opts.MinInitialDelayMs != nil {
		bInit.Min = *opts.MinInitialDelayMs
	}
	if opts.MaxInitialDelayMs != nil {
		bInit.Max = *opts.MaxInitialDelayMs
	}

	if err := bInit.Validate(); err != nil {
		return nil, fmt.Errorf("invalid initial delay bounds: %v", err)
	}
	if err := bSize.Validate(); err != nil {
		return nil, fmt.Errorf("invalid packet size bounds: %v", err)
	}
	if err := bDelay.Validate(); err != nil {
		return nil, fmt.Errorf("invalid packet delay bounds: %v", err)
	}

	return &ShapingConfig{
		InitGenerator:  &bInit,
		SizeGenerator:  &bSize,
		DelayGenerator: &bDelay,
	}, nil
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
	PacketsSent    int
	StartTime      time.Time
	Context        map[string]any
	Rand           *rand.Rand
	TargetDeadline time.Time

	SizeGenerator  Generator
	DelayGenerator Generator
}

// InitialDelay computes delay prior to transmitting headers/first byte.
func (c *ShapingConfig) InitialDelay(seed int64) time.Duration {
	r := rand.New(rand.NewSource(seed))
	delayMs := c.InitGenerator.Next(r)
	return time.Duration(delayMs) * time.Millisecond
}

// NextPacket determines the size and delay of the next transmission packet.
func (c *ShapingConfig) NextPacket(state *StreamState) (int, time.Duration) {
	pSize := int(state.SizeGenerator.Next(state.Rand))
	pDelayMs := state.DelayGenerator.Next(state.Rand)
	return pSize, time.Duration(pDelayMs) * time.Millisecond
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
	cfg *ShapingConfig,
	seed int64,
) (int64, error) {
	flusher, canFlush := w.(http.Flusher)

	var sizeGen Generator
	var delayGen Generator

	if cfg != nil {
		sizeGen = cfg.SizeGenerator
		delayGen = cfg.DelayGenerator
	} else {
		sizeGen = &LinearGenerator{Min: 1024, Max: 1024}
		delayGen = &LinearGenerator{Min: 0, Max: 0}
	}

	now := time.Now()
	state := &StreamState{
		StartTime:      now,
		Context:        make(map[string]any),
		Rand:           rand.New(rand.NewSource(seed)),
		TargetDeadline: now,
		SizeGenerator:  sizeGen,
		DelayGenerator: delayGen,
	}

	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	buf := *bufPtr

	packetTimer := time.NewTimer(0)
	if !packetTimer.Stop() {
		select {
		case <-packetTimer.C:
		default:
		}
	}

	for {
		if ctx.Err() != nil {
			return state.TotalBytesSent, ctx.Err()
		}

		done, err := transmitNextPacket(
			ctx, w, r, flusher, canFlush, cfg, state, buf, packetTimer)
		if err != nil || done {
			return state.TotalBytesSent, err
		}
	}
}

func transmitNextPacket(
	ctx context.Context,
	w http.ResponseWriter,
	r io.Reader,
	flusher http.Flusher,
	canFlush bool,
	cfg *ShapingConfig,
	state *StreamState,
	buf []byte,
	packetTimer *time.Timer,
) (bool, error) {
	packetSize, packetDelay := cfg.NextPacket(state)
	if packetSize <= 0 {
		packetSize = 1024
	}
	if packetSize > len(buf) {
		packetSize = len(buf)
	}

	n, readErr := r.Read(buf[:packetSize])
	if n == 0 && readErr != nil {
		if readErr == io.EOF {
			return true, nil
		}
		return true, readErr
	}

	if packetDelay > 0 {
		state.TargetDeadline = state.TargetDeadline.Add(packetDelay)
		delay := time.Until(state.TargetDeadline)
		if delay > 0 {
			packetTimer.Reset(delay)
			select {
			case <-packetTimer.C:
			case <-ctx.Done():
				packetTimer.Stop()
				return true, ctx.Err()
			}
		}
	}

	wBytes, writeErr := w.Write(buf[:n])
	state.TotalBytesSent += int64(wBytes)
	state.PacketsSent++
	fmt.Fprint(os.Stderr, ".")

	if canFlush {
		flusher.Flush()
	}

	if writeErr != nil {
		return true, writeErr
	}

	if readErr != nil {
		if readErr == io.EOF {
			return true, nil
		}
		return true, readErr
	}

	return false, nil
}
