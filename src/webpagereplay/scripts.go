// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webpagereplay

import (
	"bytes"
	"strconv"
)

// ReplaceConstants replaces the WPR time-seed and Math.random() placeholders
// in an injected script with the given values. Both the legacy
// {{WPR_...}} format and the new bare format are replaced.
func ReplaceConstants(
	filename string, script []byte, timeSeedMs int64, constantMathRandomResult *float64) []byte {

	randomResultStr := "null"
	if constantMathRandomResult != nil {
		randomResultStr = strconv.FormatFloat(*constantMathRandomResult, 'f', -1, 64)
	}

	timeSeedTimestamp := strconv.FormatInt(timeSeedMs, 10)
	// Legacy format, kept for backwards compatibility. These must be applied first,
	// since the new formats are substrings.
	script =
		bytes.Replace(script, []byte("{{WPR_TIME_SEED_TIMESTAMP}}"), []byte(timeSeedTimestamp), -1)
	script =
		bytes.Replace(script, []byte("{{WPR_CONSTANT_RANDOM_RESULT}}"), []byte(randomResultStr), -1)
	// New format.
	script = bytes.Replace(script, []byte("WPR_TIME_SEED_TIMESTAMP"), []byte(timeSeedTimestamp), -1)
	script = bytes.Replace(script, []byte("WPR_CONSTANT_RANDOM_RESULT"), []byte(randomResultStr), -1)
	return script
}
