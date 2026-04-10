// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/urfave/cli/v2"
	"go.chromium.org/webpagereplay/src/webpagereplay"
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
				logLevel:                  "INFO",
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

			err := common.CheckArgsAndSetLogLevel(c)
			if (err == nil) != tt.expectSuccess {
				t.Errorf("CheckArgs() error = %v, expectSuccess %v",
					err, tt.expectSuccess)
			}
		})
	}
}

func TestReplaceConstantsIfDeterministicJs(t *testing.T) {
	ptr := func(f float64) *float64 { return &f }
	tests := []struct {
		name     string
		filename string
		content  string
		timeSeed int64
		random   *float64
		want     string
	}{
		{
			name:     "deterministic.js with random result",
			filename: "deterministic.js",
			content: "const timeSeed = WPR_TIME_SEED_TIMESTAMP; " +
				"const random = WPR_CONSTANT_RANDOM_RESULT;",
			timeSeed: 12345,
			random:   ptr(0.5),
			want:     "const timeSeed = 12345; const random = 0.5;",
		},
		{
			name:     "deterministic.js with null random result",
			filename: "/path/to/deterministic.js",
			content: "const timeSeed = WPR_TIME_SEED_TIMESTAMP; " +
				"const random = WPR_CONSTANT_RANDOM_RESULT;",
			timeSeed: 12345,
			random:   nil,
			want:     "const timeSeed = 12345; const random = null;",
		},
		{
			name:     "other script file",
			filename: "other.js",
			content: "const timeSeed = WPR_TIME_SEED_TIMESTAMP; " +
				"const random = WPR_CONSTANT_RANDOM_RESULT;",
			timeSeed: 12345,
			random:   ptr(0.5),
			want: "const timeSeed = WPR_TIME_SEED_TIMESTAMP; " +
				"const random = WPR_CONSTANT_RANDOM_RESULT;",
		},
		{
			name:     "deterministic.js with legacy format",
			filename: "deterministic.js",
			content: "const timeSeed1 = WPR_TIME_SEED_TIMESTAMP; " +
				"const random1 = WPR_CONSTANT_RANDOM_RESULT; " +
				"const timeSeed2 = {{WPR_TIME_SEED_TIMESTAMP}}; " +
				"const random2 = {{WPR_CONSTANT_RANDOM_RESULT}};",
			timeSeed: 12345,
			random:   ptr(0.5),
			want: "const timeSeed1 = 12345; const random1 = 0.5; " +
				"const timeSeed2 = 12345; const random2 = 0.5;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(replaceConstantsIfDeterministicJs(
				tt.filename, []byte(tt.content), tt.timeSeed, tt.random))
			if got != tt.want {
				t.Errorf("replaceConstantsIfDeterministicJs(%s) = %s, want %s", tt.content, got, tt.want)
			}
		})
	}
}

func TestProcessInjectedScriptsForRecording(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "test.js")
	scriptContent := "console.log('test');"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("failed to write temp script: %v", err)
	}

	common := &CommonConfig{
		injectScripts: scriptPath,
	}
	archive := &webpagereplay.Archive{
		InjectedScripts: make(map[string]string),
	}

	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForRecording(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForRecording failed: %v", err)
	}

	if len(archive.InjectedScripts) != 1 {
		t.Errorf("expected 1 injected script in archive, got %d", len(archive.InjectedScripts))
	} else {
		name := filepath.Base(scriptPath)
		if contents, ok := archive.InjectedScripts[name]; !ok {
			t.Errorf("expected script %s in map", name)
		} else if contents != scriptContent {
			t.Errorf("expected content %s, got %s", scriptContent, contents)
		}
	}

	if archive.DeterministicTimeSeedMs == 0 {
		t.Error("DeterministicTimeSeedMs should be non-zero")
	}

	if len(common.transformers) != 1 {
		t.Errorf("expected 1 transformer, got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForRecording_DuplicateNameError(t *testing.T) {
	tempDir := t.TempDir()
	dir1 := filepath.Join(tempDir, "dir1")
	dir2 := filepath.Join(tempDir, "dir2")
	os.Mkdir(dir1, 0755)
	os.Mkdir(dir2, 0755)

	script1 := filepath.Join(dir1, "test.js")
	script2 := filepath.Join(dir2, "test.js")
	os.WriteFile(script1, []byte("s1"), 0644)
	os.WriteFile(script2, []byte("s2"), 0644)

	common := &CommonConfig{
		injectScripts: script1 + "," + script2,
	}
	archive := &webpagereplay.Archive{
		InjectedScripts: make(map[string]string),
	}

	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	err := common.ProcessInjectedScriptsForRecording(c, archive)
	if err == nil {
		t.Fatal("Expected error due to duplicate script names, got nil.")
	}

	if !errors.Is(err, ErrDuplicateScriptName) {
		t.Errorf("Expected error %v, got %v", ErrDuplicateScriptName, err)
	}
}

func TestProcessInjectedScriptsForReplay_SingleScriptInArchive(t *testing.T) {
	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"archived.js": "console.log('archived');",
		},
	}
	common := &CommonConfig{}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForReplay(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	if len(common.transformers) != 1 {
		t.Errorf("expected 1 transformer, got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForReplay_MultipleScriptsInArchive(t *testing.T) {
	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"s1.js": "console.log('s1');",
			"s2.js": "console.log('s2');",
		},
	}
	common := &CommonConfig{}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForReplay(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	if len(common.transformers) != 2 {
		t.Errorf("expected 2 transformers, got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForReplay_DiskOverride(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "override.js")
	scriptContent := "console.log('override');"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("failed to write temp script: %v", err)
	}

	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"archived.js": "console.log('archived');",
		},
	}
	common := &CommonConfig{
		injectScripts: scriptPath,
	}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.String("inject-scripts", scriptPath, "")
	if err := flagSet.Set("inject-scripts", scriptPath); err != nil {
		t.Fatalf("failed to set flag: %v", err)
	}
	flagSet.String("inject-archive-scripts", "false", "")
	if err := flagSet.Set("inject-archive-scripts", "false"); err != nil {
		t.Fatalf("Failed to set flag: %v", err)
	}
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForReplay(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	if len(common.transformers) != 1 {
		t.Errorf("expected 1 transformer, got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForReplay_ArchiveAndDisk(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "override.js")
	scriptContent := "console.log('override');"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("Failed to write temp script: %v", err)
	}

	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"archived.js": "console.log('archived');",
		},
	}
	common := &CommonConfig{
		injectScripts: scriptPath,
	}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.String("inject-scripts", scriptPath, "")
	if err := flagSet.Set("inject-scripts", scriptPath); err != nil {
		t.Fatalf("Failed to set flag: %v", err)
	}
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForReplay(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	// Expecting 2: one from archive, one from disk.
	if len(common.transformers) != 2 {
		t.Errorf("Expected 2 transformers, got %d", len(common.transformers))
	}
}

func verifyScriptNameCollision(t *testing.T, injectArchiveScripts string, expectError bool, expectedTransformers int) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "collision.js")
	scriptContent := "console.log('collision');"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("Failed to write temp script: %v", err)
	}

	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"collision.js": "console.log('archived collision');",
		},
	}
	common := &CommonConfig{
		injectScripts: scriptPath,
	}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.String("inject-scripts", scriptPath, "")
	if err := flagSet.Set("inject-scripts", scriptPath); err != nil {
		t.Fatalf("Failed to set flag: %v", err)
	}

	if injectArchiveScripts != "" {
		flagSet.String("inject-archive-scripts", injectArchiveScripts, "")
		if err := flagSet.Set("inject-archive-scripts", injectArchiveScripts); err != nil {
			t.Fatalf("Failed to set flag: %v", err)
		}
	}

	c := cli.NewContext(nil, flagSet, nil)

	err := common.ProcessInjectedScriptsForReplay(c, archive)

	if expectError {
		if err == nil {
			t.Fatal("Expected error due to duplicate script names, got nil.")
		}
		if !errors.Is(err, ErrDuplicateScriptName) {
			t.Errorf("Expected error %v, got %v", ErrDuplicateScriptName, err)
		}
	} else {
		if err != nil {
			t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
		}
		if len(common.transformers) != expectedTransformers {
			t.Errorf("Expected %d transformers, got %d", expectedTransformers, len(common.transformers))
		}
	}
}

func TestProcessInjectedScriptsForReplay_ScriptNameCollision(t *testing.T) {
	tests := []struct {
		name                 string
		injectArchiveScripts string // "true", "false", or "" (default)
		expectError          bool
		expectedTransformers int
	}{
		{
			name:                 "Collision errors by default",
			injectArchiveScripts: "",
			expectError:          true,
		},
		{
			name:                 "Collision errors if explicitly enabled",
			injectArchiveScripts: "true",
			expectError:          true,
		},
		{
			name:                 "No collision error if disabled",
			injectArchiveScripts: "false",
			expectError:          false,
			expectedTransformers: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifyScriptNameCollision(t, tt.injectArchiveScripts, tt.expectError, tt.expectedTransformers)
		})
	}
}

func TestProcessInjectedScriptsForReplay_InjectScriptsByHost(t *testing.T) {
	d := t.TempDir()
	s1, s2 := filepath.Join(d, "s1.js"), filepath.Join(d, "s2.js")
	os.WriteFile(s1, []byte("s1"), 0644)
	os.WriteFile(s2, []byte("s2"), 0644)

	tests := []struct {
		name, url, script string
		flags             []string
		err               bool
	}{
		{
			name:   "example",
			flags:  []string{"example.com::" + s1},
			url:    "https://example.com/",
			script: "s1",
		},
		{
			name:   "google",
			flags:  []string{"google.com::" + s2},
			url:    "https://google.com/",
			script: "s2",
		},
		{name: "error", flags: []string{"host::/no/file"}, err: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			ss := &cli.StringSlice{}
			fs.Var(ss, "inject-scripts-by-host", "")
			for _, f := range tt.flags {
				fs.Set("inject-scripts-by-host", f)
			}

			cc := &CommonConfig{}
			err := cc.ProcessInjectedScriptsForReplay(cli.NewContext(nil, fs, nil), &webpagereplay.Archive{})

			if tt.err {
				if err == nil {
					t.Fatal("Expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			req, _ := http.NewRequest("GET", tt.url, nil)
			resp := &http.Response{
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(bytes.NewReader([]byte("<html><head> </head><body></body></html>"))),
				StatusCode: http.StatusOK,
				Request:    req,
			}
			cc.transformers[0].Transform(req, resp)
			body, _ := io.ReadAll(resp.Body)
			if !bytes.Contains(body, []byte(tt.script)) {
				t.Errorf("Expected %s in %s", tt.script, body)
			}
		})
	}
}

func TestParseInjectScriptsByHost(t *testing.T) {
	tests := []struct {
		name        string
		byHost      []string
		expectError bool
	}{
		{
			name: "Valid normal host",
			byHost: []string{
				"https://example.com::/path/to/script.js",
			},
			expectError: false,
		},
		{
			name: "Valid regex host",
			byHost: []string{
				"^https?://example\\.com/.*::/path/to/script.js",
			},
			expectError: false,
		},
		{
			name: "Multiple files to same host",
			byHost: []string{
				"host::file1.js",
				"host::file2.js",
			},
			expectError: false,
		},
		{
			name: "Same file to multiple hosts",
			byHost: []string{
				"host1::file.js",
				"host2::file.js",
			},
			expectError: false,
		},
		{
			name: "Invalid format",
			byHost: []string{
				"invalid_format",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseInjectScriptsByHost(tt.byHost)
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
			}
		})
	}
}

func TestProcessInjectedScriptsForReplay_RulesFile(t *testing.T) {
	tempDir := t.TempDir()

	scriptPath1 := filepath.Join(tempDir, "script1.js")
	if err := os.WriteFile(scriptPath1, []byte("console.log('script1');"), 0644); err != nil {
		t.Fatalf("Failed to write temp script: %v", err)
	}

	scriptPath2 := filepath.Join(tempDir, "script2.js")
	if err := os.WriteFile(scriptPath2, []byte("console.log('script2');"), 0644); err != nil {
		t.Fatalf("Failed to write temp script: %v", err)
	}

	tests := []struct {
		name              string
		rules             string
		expectError       bool
		testURL           string
		expectedScripts   []string
		unexpectedScripts []string
	}{
		{
			name: "1. Existing file to a regular host",
			rules: `[
				{"URL": "https://example.com/", "InjectedScript": "` + scriptPath1 + `"}
			]`,
			expectError:       false,
			testURL:           "https://example.com/",
			expectedScripts:   []string{"script1"},
			unexpectedScripts: []string{"script2"},
		},
		{
			name: "2. Existing file to a regex host",
			rules: `[
				{"URLPattern": "^https?://example\\.com/.*", "InjectedScript": "` + scriptPath1 + `"}
			]`,
			expectError:       false,
			testURL:           "https://example.com/foo",
			expectedScripts:   []string{"script1"},
			unexpectedScripts: []string{"script2"},
		},
		{
			name: "3. Multiple files to the same host",
			rules: `[
				{"URLPattern": "^host$", "InjectedScript": "` + scriptPath1 + `"},
				{"URLPattern": "^host$", "InjectedScript": "` + scriptPath2 + `"}
			]`,
			expectError:       false,
			testURL:           "host",
			expectedScripts:   []string{"script1", "script2"},
			unexpectedScripts: []string{},
		},
		{
			name: "4. Non-existent file",
			rules: `[
				{"URLPattern": "^host$", "InjectedScript": "/non/existent/file.js"}
			]`,
			expectError: true,
		},
		{
			name: "5. Same file to multiple hosts",
			rules: `[
				{"URLPattern": "^host1$", "InjectedScript": "` + scriptPath1 + `"},
				{"URLPattern": "^host2$", "InjectedScript": "` + scriptPath1 + `"}
			]`,
			expectError:       false,
			testURL:           "host1",
			expectedScripts:   []string{"script1"},
			unexpectedScripts: []string{"script2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rulesFilePath := filepath.Join(t.TempDir(), "rules.json")
			if err := os.WriteFile(rulesFilePath, []byte(tt.rules), 0644); err != nil {
				t.Fatalf("Failed to write rules file: %v", err)
			}

			archive := &webpagereplay.Archive{}
			common := &CommonConfig{}

			flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
			flagSet.String("rules-file", "", "")
			if err := flagSet.Set("rules-file", rulesFilePath); err != nil {
				t.Fatalf("Failed to set flag: %v", err)
			}

			c := cli.NewContext(nil, flagSet, nil)

			err := common.ProcessInjectedScriptsForReplay(c, archive)
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
			}

			if !tt.expectError && tt.testURL != "" {
				if len(common.transformers) != 1 {
					t.Fatalf("Expected 1 transformer, got %d", len(common.transformers))
				}
				rbt := common.transformers[0]

				req, _ := http.NewRequest("GET", tt.testURL, nil)
				resp := &http.Response{
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader([]byte("<html><head></head></html>"))),
					StatusCode: http.StatusOK,
					Request:    req,
				}
				resp.Header.Set("Content-Type", "text/html")

				rbt.Transform(req, resp)

				body, _ := io.ReadAll(resp.Body)
				for _, expected := range tt.expectedScripts {
					if !bytes.Contains(body, []byte(expected)) {
						t.Errorf("Expected script %q to be injected, body: %s", expected, body)
					}
				}
				for _, unexpected := range tt.unexpectedScripts {
					if bytes.Contains(body, []byte(unexpected)) {
						t.Errorf("Did not expect script %q to be injected, body: %s", unexpected, body)
					}
				}
			}
		})
	}
}

func TestProcessInjectedScriptsForReplay_Fallback(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "fallback.js")
	scriptContent := "console.log('fallback');"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("failed to write temp script: %v", err)
	}

	archive := &webpagereplay.Archive{
		InjectedScripts: make(map[string]string),
	}
	common := &CommonConfig{
		injectScripts: scriptPath,
	}
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	if err := common.ProcessInjectedScriptsForReplay(c, archive); err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	if len(common.transformers) != 1 {
		t.Errorf("expected 1 transformer, got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForReplay_SkipDiskDefaultWhenInArchive(t *testing.T) {
	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"deterministic.js": "console.log('archived deterministic');",
		},
	}

	// Simulate the default state where 'inject-scripts' flag is not explicitly
	// set but defaults to "deterministic.js".
	common := &CommonConfig{
		injectScripts: "deterministic.js",
	}

	// Create an empty FlagSet and Context to simulate that the flag was not set.
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	c := cli.NewContext(nil, flagSet, nil)

	err := common.ProcessInjectedScriptsForReplay(c, archive)

	if err != nil {
		t.Fatalf("ProcessInjectedScriptsForReplay failed: %v", err)
	}

	// We expect exactly 1 transformer (from the archive script). The default
	// script from the file system should have been skipped.
	if len(common.transformers) != 1 {
		t.Errorf("Expected 1 transformer (from archive), got %d", len(common.transformers))
	}
}

func TestProcessInjectedScriptsForReplay_ExplicitFlagCollidesWithArchive(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "deterministic.js")
	os.WriteFile(scriptPath, []byte("console.log('disk');"), 0644)

	archive := &webpagereplay.Archive{
		InjectedScripts: map[string]string{
			"deterministic.js": "console.log('archived');",
		},
	}

	common := &CommonConfig{
		injectScripts: scriptPath,
	}

	// Simulate the user explicitly setting the 'inject-scripts' flag.
	// We must register the flag with the FlagSet before we can set its value.
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	flagSet.String("inject-scripts", "", "")
	flagSet.Set("inject-scripts", scriptPath)

	c := cli.NewContext(nil, flagSet, nil)

	err := common.ProcessInjectedScriptsForReplay(c, archive)

	if err == nil {
		t.Fatal("Expected error due to duplicate script names, got nil.")
	}

	if !errors.Is(err, ErrDuplicateScriptName) {
		t.Errorf("Expected error %v, got %v", ErrDuplicateScriptName, err)
	}
}
