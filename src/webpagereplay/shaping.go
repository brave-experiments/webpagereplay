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
	"strings"
	"sync"
	"time"
)

// ShapingConfig holds pre-initialized generators for zero-overhead streaming.
type ShapingConfig struct {
	InitialDelay Generator
	Sizes        Generator
	Delays       Generator
}

// NetworkPresets provides pre-configured baseline values and
// profile parameters.
var NetworkPresets = map[string]ShapingConfig{
	"3g": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 100, Max: 250,
			MidProb: 0.035, Mid: 600,
			SlowProb: 0.015, Slow: 1000,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 4380,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 80, Max: 120,
			MidProb: 0.035, Mid: 600,
			SlowProb: 0.015, Slow: 1000,
		}.New(),
	},
	"4g": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 30, Max: 70,
			MidProb: 0.045, Mid: 250,
			SlowProb: 0.005, Slow: 100,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 14600,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 35, Max: 50,
			MidProb: 0.045, Mid: 250,
			SlowProb: 0.005, Slow: 100,
		}.New(),
	},
	"5g": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 10, Max: 25,
			MidProb: 0.049, Mid: 80,
			SlowProb: 0.001, Slow: 20,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 65535,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 15, Max: 25,
			MidProb: 0.049, Mid: 80,
			SlowProb: 0.001, Slow: 20,
		}.New(),
	},
	"dsl": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 20, Max: 50,
			MidProb: 0.049, Mid: 150,
			SlowProb: 0.001, Slow: 50,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 8760,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 10, Max: 18,
			MidProb: 0.049, Mid: 150,
			SlowProb: 0.001, Slow: 50,
		}.New(),
	},
	"cable": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 15, Max: 30,
			MidProb: 0.049, Mid: 150,
			SlowProb: 0.001, Slow: 50,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 32768,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 10, Max: 18,
			MidProb: 0.049, Mid: 150,
			SlowProb: 0.001, Slow: 50,
		}.New(),
	},
	"satellite": {
		InitialDelay: HeavyTailedLatencyConfig{
			Min: 500, Max: 700,
			MidProb: 0.045, Mid: 120,
			SlowProb: 0.005, Slow: 100,
		}.New(),
		Sizes: BimodalTrafficConfig{
			AckProb: 0.45, MaxProb: 0.45,
			AckMin: 40, AckMax: 100,
			Mid: 101, Max: 32768,
		}.New(),
		Delays: HeavyTailedLatencyConfig{
			Min: 30, Max: 45,
			MidProb: 0.045, Mid: 120,
			SlowProb: 0.005, Slow: 100,
		}.New(),
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
	MinPacketSizeBytes *int64
	MaxPacketSizeBytes *int64
	MinPacketDelayMs   *int64
	MaxPacketDelayMs   *int64
}

func ptrInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// CreateShapingConfig validates CLI inputs and constructs the runtime profile.
func CreateShapingConfig(opts ShapingOptions) (cfg *ShapingConfig, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("shaping config error: %v", r)
		}
	}()

	if opts.Preset != "" {
		return createPresetShapingConfig(opts)
	}

	initMin := ptrInt64(opts.MinInitialDelayMs)
	initMax := ptrInt64(opts.MaxInitialDelayMs)
	sizeMin := ptrInt64(opts.MinPacketSizeBytes)
	sizeMax := ptrInt64(opts.MaxPacketSizeBytes)
	delayMin := ptrInt64(opts.MinPacketDelayMs)
	delayMax := ptrInt64(opts.MaxPacketDelayMs)

	if sizeMax == 0 && sizeMin == 0 {
		sizeMin = 1024
		sizeMax = 1024
	}

	uniInit := UniformConfig{Min: initMin, Max: initMax}.New()
	uniSize := UniformConfig{Min: sizeMin, Max: sizeMax}.New()
	uniDelay := UniformConfig{Min: delayMin, Max: delayMax}.New()

	if opts.MinInitialDelayMs == nil && opts.MaxInitialDelayMs == nil &&
		opts.MinPacketSizeBytes == nil && opts.MaxPacketSizeBytes == nil &&
		opts.MinPacketDelayMs == nil && opts.MaxPacketDelayMs == nil {
		return nil, nil
	}

	return &ShapingConfig{
		InitialDelay: uniInit,
		Sizes:        uniSize,
		Delays:       uniDelay,
	}, nil
}

func createPresetShapingConfig(
	opts ShapingOptions,
) (cfg *ShapingConfig, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("shaping preset error: %v", r)
		}
	}()

	base, ok := NetworkPresets[strings.ToLower(opts.Preset)]
	if !ok {
		return nil, fmt.Errorf("invalid preset: %s", opts.Preset)
	}

	bSizeGen := base.Sizes.(*BimodalTrafficGenerator)
	bSizeCfg := bSizeGen.BimodalTrafficConfig
	bDelayGen := base.Delays.(*HeavyTailedLatencyGenerator)
	bDelayCfg := bDelayGen.HeavyTailedLatencyConfig
	bInitGen := base.InitialDelay.(*HeavyTailedLatencyGenerator)
	bInitCfg := bInitGen.HeavyTailedLatencyConfig

	if opts.MinPacketSizeBytes != nil {
		bSizeCfg.AckMin = *opts.MinPacketSizeBytes
	}
	if opts.MaxPacketSizeBytes != nil {
		bSizeCfg.Max = *opts.MaxPacketSizeBytes
	}
	if opts.MinPacketDelayMs != nil {
		bDelayCfg.Min = *opts.MinPacketDelayMs
	}
	if opts.MaxPacketDelayMs != nil {
		bDelayCfg.Max = *opts.MaxPacketDelayMs
	}
	if opts.MinInitialDelayMs != nil {
		bInitCfg.Min = *opts.MinInitialDelayMs
	}
	if opts.MaxInitialDelayMs != nil {
		bInitCfg.Max = *opts.MaxInitialDelayMs
	}

	newInit := bInitCfg.New()
	newSize := bSizeCfg.New()
	newDelay := bDelayCfg.New()

	return &ShapingConfig{
		InitialDelay: newInit,
		Sizes:        newSize,
		Delays:       newDelay,
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

	Sizes  Generator
	Delays Generator
}

// GetInitialDelay computes delay prior to transmitting headers/first byte.
func (c *ShapingConfig) GetInitialDelay(r *rand.Rand) time.Duration {
	delayMs := c.InitialDelay.Next(r)
	return time.Duration(delayMs) * time.Millisecond
}

// NextPacket determines the size and delay of the next transmission packet.
// Note: This method accesses generators on the StreamState rather than the
// ShapingConfig receiver, making it safe to call even when the ShapingConfig
// pointer is nil.
func (c *ShapingConfig) NextPacket(state *StreamState) (int, time.Duration) {
	pSize := int(state.Sizes.Next(state.Rand))
	if pSize <= 0 {
		pSize = 1024
	}
	pDelayMs := state.Delays.Next(state.Rand)
	return pSize, time.Duration(pDelayMs) * time.Millisecond
}

var streamBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func newStoppedTimer() *time.Timer {
	t := time.NewTimer(0)
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	return t
}

func enforcePacingDelay(
	ctx context.Context,
	state *StreamState,
	timer *time.Timer,
	delay time.Duration,
) error {
	if delay <= 0 {
		return nil
	}
	state.TargetDeadline = state.TargetDeadline.Add(delay)
	d := time.Until(state.TargetDeadline)
	if d > 0 {
		timer.Reset(d)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return nil
}

// StreamShapedResponse handles hyper-efficient streaming of shaped responses
// using buffer pooling and absolute target pacing.
func StreamShapedResponse(
	ctx context.Context,
	w http.ResponseWriter,
	r io.Reader,
	cfg *ShapingConfig,
	rnd *rand.Rand,
) (int64, error) {
	flusher, canFlush := w.(http.Flusher)

	var sizeGen Generator
	var delayGen Generator

	if cfg != nil {
		sizeGen = cfg.Sizes
		delayGen = cfg.Delays
	} else {
		sizeGen = UniformConfig{Min: 1024, Max: 1024}.New()
		delayGen = UniformConfig{Min: 0, Max: 0}.New()
	}

	now := time.Now()
	state := &StreamState{
		StartTime:      now,
		Rand:           rnd,
		TargetDeadline: now,
		Sizes:          sizeGen,
		Delays:         delayGen,
	}

	bufPtr := streamBufferPool.Get().(*[]byte)
	defer streamBufferPool.Put(bufPtr)
	buf := *bufPtr

	packetTimer := newStoppedTimer()

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

	err := enforcePacingDelay(ctx, state, packetTimer, packetDelay)
	if err != nil {
		return true, err
	}

	wBytes, writeErr := w.Write(buf[:n])
	state.TotalBytesSent += int64(wBytes)
	state.PacketsSent++

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

// ShapedReadCloser wraps an io.ReadCloser to enforce shaping delay and
// bandwidth policies.
type ShapedReadCloser struct {
	underlying  io.ReadCloser
	ctx         context.Context
	cfg         *ShapingConfig
	state       *StreamState
	packetTimer *time.Timer
}

func (s *ShapedReadCloser) Read(p []byte) (int, error) {
	if s.ctx.Err() != nil {
		return 0, s.ctx.Err()
	}

	packetSize, packetDelay := s.cfg.NextPacket(s.state)
	if packetSize > len(p) {
		packetSize = len(p)
	}

	n, readErr := s.underlying.Read(p[:packetSize])
	if n == 0 && readErr != nil {
		return 0, readErr
	}

	err := enforcePacingDelay(s.ctx, s.state, s.packetTimer, packetDelay)
	if err != nil {
		return n, err
	}

	s.state.TotalBytesSent += int64(n)
	s.state.PacketsSent++

	return n, readErr
}

func (s *ShapedReadCloser) Close() error {
	s.packetTimer.Stop()
	return s.underlying.Close()
}

// WrapShapedReadCloser wraps an io.ReadCloser to enforce shaping delay and
// bandwidth policies.
func WrapShapedReadCloser(
	ctx context.Context,
	rc io.ReadCloser,
	cfg *ShapingConfig,
	rnd *rand.Rand,
) io.ReadCloser {
	now := time.Now()
	state := &StreamState{
		StartTime:      now,
		Rand:           rnd,
		TargetDeadline: now,
		Sizes:          cfg.Sizes,
		Delays:         cfg.Delays,
	}

	return &ShapedReadCloser{
		underlying:  rc,
		ctx:         ctx,
		cfg:         cfg,
		state:       state,
		packetTimer: newStoppedTimer(),
	}
}
