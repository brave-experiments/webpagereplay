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

// LinearGenerator computes uniform linear integer values between limits.
//
// Distribution Profile (Uniform Linear):
// Freq |
//
//	|   +-------------------+
//	|   |                   |
//	+---+-------------------+---> Value
//	   Min                 Max
//
// Profile Explanations:
//   - Uniformity: Guarantees a perfectly uniform distribution across bounds.
//   - Pacing Fallback: Used as baseline behavior when custom parameters are set
//     standalone without heavy-tailed empirical network emulation profiles.
type LinearGenerator struct {
	Min int64
	Max int64
}

func randomRange(r *rand.Rand, min, max int64) int64 {
	if max <= min {
		return min
	}
	return min + r.Int63n(max-min+1)
}

// Next returns a uniform random integer between min and max.
func (l *LinearGenerator) Next(r *rand.Rand) int64 {
	val := randomRange(r, l.Min, l.Max)
	if val < 0 {
		return 0
	}
	return val
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

// Validate ensures configuration limits are correct and in bounds.
func (l *LinearGenerator) Validate() error {
	return validateBounds(l.Min, l.Max)
}

// BimodalGenerator computes bimodal values simulating physical network models.
//
// Distribution Profile (Bimodal Empirical CAIDA):
// Freq |
//
//	TinyProb |   +-----+
//	         |   | ACK |            +-----+-----+
//	Mid      |   |     |            | Mid | Max | (MaxProb)
//	         |   |     |            |     |     |
//	         +---+-----+------------+-----+-----+---> Size
//	          TinyMin-Max          Mid        Max
//
// Profile Explanations:
// - ACK (TinyMin-Max): Simulates tiny TCP control / acknowledgment frames.
// - Mid (Mid): Models intermediate payload transfer chunk packets.
// - Max (Max): Simulates max payload framing hitting interface MTU ceilings.
type BimodalGenerator struct {
	TinyProb float64
	TinyMin  int64
	TinyMax  int64
	Mid      int64
	MaxProb  float64
	Max      int64
}

// Next computes empirical bimodal values based on real network flows.
func (b *BimodalGenerator) Next(r *rand.Rand) int64 {
	var val int64
	roll := r.Float64()
	if roll < b.TinyProb {
		val = randomRange(r, b.TinyMin, b.TinyMax)
	} else if roll >= b.MaxProb {
		val = randomRange(r, b.Mid, b.Max-1)
	} else {
		val = b.Max
	}
	if val <= 0 {
		return 1024
	}
	return val
}

// Validate verifies bimodal packet structure parameters.
func (b *BimodalGenerator) Validate() error {
	if err := validateProbability(b.TinyProb); err != nil {
		return err
	}
	if err := validateProbability(b.MaxProb); err != nil {
		return err
	}
	if b.TinyProb > b.MaxProb {
		return fmt.Errorf("tiny probability cannot exceed MTU threshold")
	}
	if err := validateBounds(b.TinyMin, b.TinyMax); err != nil {
		return fmt.Errorf("tiny bounds invalid: %v", err)
	}
	if err := validateBounds(b.Mid, b.Max); err != nil {
		return fmt.Errorf("size bounds invalid: %v", err)
	}
	return nil
}

// HeavyTailedGenerator computes base values plus heavy-tailed outage stalls.
//
// Distribution Profile (Heavy-Tailed Outage & Jitter Spikes):
// Freq |
//
//	95% |   +-----+
//	 5% |   |     |       +-----+ (JitterProb)
//
// 0.1% |   |     |       |     |        +-----+ (StallProb)
//
//	+---+-----+-------+-----+--------+-----+---> Time
//	   Min   Max     +JitterSpike    +Stall
//
// Profile Explanations:
//   - Base RTT (Min-Max): Ongoing inter-packet structural delivery baseline.
//   - Jitter (JitterProb): Models TCP window scaling bufferbloat latency spikes.
//   - Stall (StallProb): Simulates deep link freezing, radio resource control
//     states, or temporary handover disconnection drops.
type HeavyTailedGenerator struct {
	Min         int64
	Max         int64
	JitterProb  float64
	JitterSpike int64
	StallProb   float64
	Stall       int64
}

// Next calculates base values plus customizable heavy-tailed outage stalls.
func (h *HeavyTailedGenerator) Next(r *rand.Rand) int64 {
	val := randomRange(r, h.Min, h.Max)

	prob := r.Float64()
	if prob < h.StallProb {
		val += h.Stall
	} else if prob < h.JitterProb {
		val += h.JitterSpike
	}

	return val
}

// Validate verifies heavy-tailed latency spike parameters.
func (h *HeavyTailedGenerator) Validate() error {
	if err := validateBounds(h.Min, h.Max); err != nil {
		return err
	}
	if err := validateProbability(h.JitterProb); err != nil {
		return err
	}
	if err := validateProbability(h.StallProb); err != nil {
		return err
	}
	if h.JitterSpike < 0 || h.Stall < 0 {
		return fmt.Errorf("spike and stall durations cannot be negative")
	}
	return nil
}
