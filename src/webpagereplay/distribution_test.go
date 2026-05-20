// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"testing"
)

func TestGenerator_Validate(t *testing.T) {
	if (&LinearGenerator{Min: -1, Max: 10}).Validate() == nil {
		t.Errorf("LinearGenerator failed to validate negative min")
	}
	if (&LinearGenerator{Min: 10, Max: 5}).Validate() == nil {
		t.Errorf("LinearGenerator failed to validate min > max")
	}
	if (&LinearGenerator{Min: 10, Max: 0}).Validate() == nil {
		t.Errorf("LinearGenerator failed to validate min > 0 max")
	}
	if (&BimodalGenerator{TinyProb: 2.0}).Validate() == nil {
		t.Errorf("BimodalGenerator failed to validate probability > 1.0")
	}
	if (&BimodalGenerator{TinyMin: -5}).Validate() == nil {
		t.Errorf("BimodalGenerator failed to validate negative size")
	}
	if (&BimodalGenerator{Mid: 5000, Max: 1500}).Validate() == nil {
		t.Errorf("BimodalGenerator failed to validate Mid > Max")
	}
	if (&HeavyTailedGenerator{JitterProb: 5.0}).Validate() == nil {
		t.Errorf("HeavyTailedGenerator failed to validate prob > 1.0")
	}
	if (&HeavyTailedGenerator{JitterSpike: -10}).Validate() == nil {
		t.Errorf("HeavyTailedGenerator failed to validate negative spike")
	}
	if (&HeavyTailedGenerator{Max: 50, JitterSpike: 150}).Validate() != nil {
		t.Errorf("HeavyTailedGenerator failed on valid JitterSpike order")
	}
}
