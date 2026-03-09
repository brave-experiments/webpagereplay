// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"flag"
	"strconv"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestCommonConfig_CheckArgs(t *testing.T) {
	ptr := func(f float64) *float64 { return &f }
	tests := []struct {
		name          string
		val           *float64
		expectSuccess bool
	}{
		{
			name:          "The minimum value is valid",
			val:           ptr(0.0),
			expectSuccess: true,
		},
		{
			name:          "Values within the permissible range are valid",
			val:           ptr(0.5),
			expectSuccess: true,
		},
		{
			name:          "The supremum value is invalid",
			val:           ptr(1.0),
			expectSuccess: false,
		},
		{
			name:          "Values below the minimum value are invalid",
			val:           ptr(-0.1),
			expectSuccess: false,
		},
		{
			name:          "Not setting a value is valid",
			val:           nil,
			expectSuccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val := 0.0
			if tt.val != nil {
				val = *tt.val
			}

			common := &CommonConfig{
				constantMathRandomResult:  val,
				httpPort:                  8080,
				skipCertLoadingForTesting: true,
			}

			flagSet := flag.NewFlagSet("test", 0)
			flagSet.Float64("constant-math-random-result", val, "")

			args := []string{}
			if tt.val != nil {
				args = append(
					args, "--constant-math-random-result",
					strconv.FormatFloat(val, 'f', -1, 64))
			}
			args = append(args, "archive.json")
			flagSet.Parse(args)
			c := cli.NewContext(nil, flagSet, nil)

			err := common.CheckArgs(c)
			if (err == nil) != tt.expectSuccess {
				t.Errorf("CheckArgs() error = %v, expectSuccess %v",
					err, tt.expectSuccess)
			}
		})
	}
}
