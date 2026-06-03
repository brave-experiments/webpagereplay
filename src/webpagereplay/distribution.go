// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"fmt"
	"math/rand"
)

// Generator defines the contract for numerical traffic distribution engines.
type Generator interface {
	Next(r *rand.Rand) int64
}

func randomRange(r *rand.Rand, min, max int64) int64 {
	if max <= min {
		return min
	}
	return min + r.Int63n(max-min+1)
}

func validateBounds(min, max int64) error {
	if min < 0 || max < 0 {
		return fmt.Errorf("limits cannot be negative")
	}
	if min > max {
		return fmt.Errorf("min cannot be greater than max")
	}
	return nil
}

func validateProbability(p float64) error {
	if p < 0 || p > 1 {
		return fmt.Errorf("probabilities must be in [0, 1]")
	}
	return nil
}

// UniformGenerator computes uniform linear integer values between limits.
//
// Probabilities:
//
//	|    +---------------------+ (Uniform)
//	|    |#####################|
//	|    |#####################|
//	|    |#####################|
//	+----+---------------------+
//
// Ranges:         [Min ............. Max]
//
//	+----+---------------------+
type UniformConfig struct {
	Min int64
	Max int64
}

type UniformGenerator struct {
	UniformConfig
}

// Next returns a uniform random integer between min and max.
func (u *UniformGenerator) Next(r *rand.Rand) int64 {
	val := randomRange(r, u.Min, u.Max)
	if val < 0 {
		return 0
	}
	return val
}

// BimodalTrafficGenerator models empirical network packet distributions which
// exhibit bimodal properties with two distinct traffic peaks (tiny control frames and
// full MTU payloads) separated by a flat, low-density intermediate range.
//
// Probabilities:
//
//	|                                         +---+ MaxProb
//	|    +---- AckProb ----+                  |###|
//	|    |#################|    +-------------+###|
//	|    |#################|    |#############|###|
//	+----+-----------------+----+-------------+---+
//
// Ranges:    |    .                 .    .             .   .
//
//	    |    .                 .    .             .   .
//	Max |    .                 .    .             [Max]
//	Mid |    .                 .    [Mid ... Max-1]   .
//	Ack |    [AckMin ... AckMax]    .             .   .
//	    +----+-----------------+----+-------------+---+
//
// Profile Explanations:
//   - ACK (AckMin-AckMax): Simulates tiny TCP control / acknowledgment frames
//   - Mid (Mid to Max-1): Models intermediate payload transfer chunk packets
//   - Max (Max): Simulates max payload framing hitting interface MTU ceilings
type BimodalTrafficConfig struct {
	AckProb float64
	AckMin  int64
	AckMax  int64
	Mid     int64
	MaxProb float64
	Max     int64
}

type BimodalTrafficGenerator struct {
	BimodalTrafficConfig

	// Pre-computed CDF threshold for fast Next().
	midThreshold float64
}

func (b *BimodalTrafficGenerator) Next(r *rand.Rand) int64 {
	var val int64
	roll := r.Float64()
	if roll < b.AckProb {
		val = randomRange(r, b.AckMin, b.AckMax)
	} else if roll < b.midThreshold {
		val = randomRange(r, b.Mid, b.Max-1)
	} else {
		val = b.Max
	}
	if val <= 0 {
		return 1024
	}
	return val
}

// HeavyTailedLatencyGenerator computes base values plus heavy-tailed outage
// stalls.
//
// Probabilities:
//
//	|    + Base Prob.  +
//	|    |#############|
//	|    |#############|      + JitterProb +
//	|    |#############|      |#############|      +  StallProb  +
//	|    |#############|      |#############|      |#############|
//	+----+-------------+------+-------------+------+-------------+
//
// Ranges:    |    .             .      .             .      .             .
//
//	 Stall |    | Stall Offset >>>>>>>>>>>>>>>>>>>>>>>>>> | Range ------|
//	Jitter |    | Jitter Offset >>>> | Range -------|     .             .
//	  Base |    [ Min ... Max ]      .              .     .             .
//	       |    | Range ------|      .              .     .             .
//	       +----+-------------+------+--------------+-----+-------------+
//
// Profile Explanations:
//   - Base: Normal, baseline transmission delay.
//   - Jitter: Short, transient latency spikes.
//   - Stall: Severe, extended link outage stalls.
type HeavyTailedLatencyConfig struct {
	Min        int64
	Max        int64
	JitterProb float64
	Jitter     int64
	StallProb  float64
	Stall      int64
}

type HeavyTailedLatencyGenerator struct {
	HeavyTailedLatencyConfig

	// Pre-computed CDF boundaries for fast Next().
	baseThreshold   float64
	jitterThreshold float64
}

func (c UniformConfig) New() *UniformGenerator {
	if err := validateBounds(c.Min, c.Max); err != nil {
		panic(fmt.Errorf("invalid uniform bounds: %v", err))
	}
	return &UniformGenerator{UniformConfig: c}
}

func (c BimodalTrafficConfig) New() *BimodalTrafficGenerator {
	if err := validateProbability(c.AckProb); err != nil {
		panic(err)
	}
	if err := validateProbability(c.MaxProb); err != nil {
		panic(err)
	}
	if c.AckProb+c.MaxProb > 1.0 {
		panic(fmt.Errorf("sum of ACK and Max probabilities cannot exceed 1.0"))
	}
	if err := validateBounds(c.AckMin, c.AckMax); err != nil {
		panic(fmt.Errorf("ACK bounds invalid: %v", err))
	}
	if err := validateBounds(c.Mid, c.Max); err != nil {
		panic(fmt.Errorf("size bounds invalid: %v", err))
	}

	midThreshold := 0.0
	if c.MaxProb > 0 {
		midThreshold = 1.0 - c.MaxProb
	}

	return &BimodalTrafficGenerator{
		BimodalTrafficConfig: c,
		midThreshold:         midThreshold,
	}
}

func (c HeavyTailedLatencyConfig) New() *HeavyTailedLatencyGenerator {
	if err := validateBounds(c.Min, c.Max); err != nil {
		panic(fmt.Errorf("invalid bounds: %v", err))
	}
	if err := validateProbability(c.JitterProb); err != nil {
		panic(fmt.Errorf("invalid jitter probability: %v", err))
	}
	if err := validateProbability(c.StallProb); err != nil {
		panic(fmt.Errorf("invalid stall probability: %v", err))
	}
	if c.StallProb+c.JitterProb > 1.0 {
		panic(fmt.Errorf("sum of Stall and Jitter probabilities cannot exceed 1.0"))
	}
	if c.Jitter < 0 || c.Stall < 0 {
		panic(fmt.Errorf("spike and stall durations cannot be negative"))
	}
	return &HeavyTailedLatencyGenerator{
		HeavyTailedLatencyConfig: c,
		baseThreshold:            1.0 - c.JitterProb - c.StallProb,
		jitterThreshold:          1.0 - c.StallProb,
	}
}

func (h *HeavyTailedLatencyGenerator) Next(r *rand.Rand) int64 {
	val := randomRange(r, h.Min, h.Max)

	prob := r.Float64()
	if prob < h.baseThreshold {
		// Return Base value (no modification).
	} else if prob < h.jitterThreshold {
		val += h.Jitter
	} else {
		val += h.Stall
	}

	return val
}
