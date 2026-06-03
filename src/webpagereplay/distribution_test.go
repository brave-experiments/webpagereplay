// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"testing"
)

func TestGenerator_Validate(t *testing.T) {
	panics := func(f func()) (p bool) {
		defer func() {
			if r := recover(); r != nil {
				p = true
			}
		}()
		f()
		return false
	}

	if !panics(func() { UniformConfig{Min: -1, Max: 10}.New() }) {
		t.Errorf("UniformGenerator failed to validate negative min")
	}
	if !panics(func() { UniformConfig{Min: 10, Max: 5}.New() }) {
		t.Errorf("UniformGenerator failed to validate min > max")
	}
	if !panics(func() { UniformConfig{Min: 10, Max: 0}.New() }) {
		t.Errorf("UniformGenerator failed to validate min > 0 max")
	}
	if !panics(func() { BimodalTrafficConfig{AckProb: 2.0}.New() }) {
		t.Errorf("BimodalTrafficGenerator failed to validate probability > 1.0")
	}
	if !panics(func() {
		BimodalTrafficConfig{AckProb: 0.6, MaxProb: 0.5}.New()
	}) {
		t.Errorf("BimodalTrafficGenerator failed to validate " +
			"sum of probabilities > 1.0")
	}
	if !panics(func() { BimodalTrafficConfig{AckMin: -5}.New() }) {
		t.Errorf("BimodalTrafficGenerator failed to validate negative size")
	}
	if !panics(func() { BimodalTrafficConfig{Mid: 5000, Max: 1500}.New() }) {
		t.Errorf("BimodalTrafficGenerator failed to validate Mid > Max")
	}

	if !panics(func() { HeavyTailedLatencyConfig{MidProb: 5.0}.New() }) {
		t.Errorf("HeavyTailedLatencyGenerator failed to validate prob > 1.0")
	}
	if !panics(func() {
		HeavyTailedLatencyConfig{SlowProb: 0.6, MidProb: 0.5}.New()
	}) {
		t.Errorf("HeavyTailedLatencyGenerator failed to validate " +
			"sum of probabilities > 1.0")
	}
	if !panics(func() { HeavyTailedLatencyConfig{Mid: -10}.New() }) {
		t.Errorf("HeavyTailedLatencyGenerator failed to " +
			"validate negative spike")
	}
	if panics(func() { HeavyTailedLatencyConfig{Max: 50, Mid: 150}.New() }) {
		t.Errorf("HeavyTailedLatencyGenerator failed on valid Mid order")
	}
}
