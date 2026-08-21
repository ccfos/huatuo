// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"huatuo-bamai/internal/log"
)

func TestValidateLoggingOptions(t *testing.T) {
	tests := []struct {
		name       string
		opts       loggingOptions
		logSizeSet bool
		wantError  string
	}{
		{
			name: "defaults",
			opts: loggingOptions{level: "error", file: "stdout", size: 100},
		},
		{
			name:       "unlimited file",
			opts:       loggingOptions{level: "info", file: "/tmp/profiler.log", size: 0},
			logSizeSet: true,
		},
		{
			name: "verbose overrides level and file",
			opts: loggingOptions{verbose: true, level: "invalid", file: "", size: 100},
		},
		{
			name:      "invalid level",
			opts:      loggingOptions{level: "fatal", file: "stdout", size: 100},
			wantError: `invalid --log-level "fatal"; allowed: trace, debug, info, warn, error`,
		},
		{
			name:      "empty file",
			opts:      loggingOptions{level: "info", file: "", size: 100},
			wantError: "--log-file must be a file path or stdout",
		},
		{
			name:       "negative size",
			opts:       loggingOptions{level: "info", file: "/tmp/profiler.log", size: -1},
			logSizeSet: true,
			wantError:  "--log-size must be at least 0 MB",
		},
		{
			name:       "explicit size with stdout",
			opts:       loggingOptions{level: "info", file: "stdout", size: 1},
			logSizeSet: true,
			wantError:  "--log-size applies only when --log-file is a file path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoggingOptions(tt.opts, tt.logSizeSet)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLoggingFlagDefaults(t *testing.T) {
	ctx := newValidationCLIContext(t)
	require.Equal(t, "error", ctx.String("log-level"))
	require.Equal(t, "stdout", ctx.String("log-file"))
	require.Equal(t, 100, ctx.Int("log-size"))
}

func TestSetupLoggingWritesFileWithoutRotation(t *testing.T) {
	log.SetOutput(io.Discard)
	log.SetLevel("info")
	t.Cleanup(func() {
		log.SetOutput(io.Discard)
		log.SetLevel("info")
	})

	path := filepath.Join(t.TempDir(), "profiler.log")
	closer, err := setupLogging(loggingOptions{
		level: "info",
		file:  path,
		size:  0,
	})
	require.NoError(t, err)
	require.NotNil(t, closer)

	log.Info("unlimited log ", strings.Repeat("x", 1024*1024+1))
	require.NoError(t, closer.Close())

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(content), "[profiler] unlimited log")
	files, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestSetupLoggingConfiguresLevels(t *testing.T) {
	t.Cleanup(func() {
		log.SetOutput(io.Discard)
		log.SetLevel("info")
	})

	for _, level := range []string{"trace", "debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			closer, err := setupLogging(loggingOptions{
				level: level,
				file:  "stdout",
				size:  100,
			})
			require.NoError(t, err)
			require.Nil(t, closer)
			want, err := logrus.ParseLevel(level)
			require.NoError(t, err)
			require.Equal(t, want, log.GetLevel())
		})
	}
}

func TestSetupLoggingRejectsUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "profiler.log")
	closer, err := setupLogging(loggingOptions{
		level: "info",
		file:  path,
		size:  100,
	})
	require.Nil(t, closer)
	require.ErrorContains(t, err, "open log file")
}
