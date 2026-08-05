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
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/toolstream"
)

func TestAppModeValidation(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{
			name:      "mode is required",
			args:      []string{"tcpshark", "--bpf-path", "unused.o"},
			wantError: "Required flag \"mode\" not set",
		},
		{
			name: "retransmit mode",
			args: []string{
				"tcpshark", "--mode", "retransmit", "--bpf-path", "unused.o",
			},
		},
		{
			name: "invalid mode",
			args: []string{
				"tcpshark", "--mode", "invalid", "--bpf-path", "unused.o",
			},
			wantError: `invalid --mode "invalid"; want "retransmit"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(func(_ *cli.Context) error { return nil })
			err := app.Run(tt.args)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestAppTLPFlag(t *testing.T) {
	tests := []struct {
		name string
		flag string
		want bool
	}{
		{name: "disabled by default"},
		{name: "long flag", flag: "--enable-tlp", want: true},
		{name: "short alias", flag: "--tlp", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var isTLPEnabled bool
			app := newTestApp(func(c *cli.Context) error {
				isTLPEnabled = c.Bool(cliFlagEnableTLP)
				return nil
			})
			args := []string{
				"tcpshark", "--mode", "retransmit", "--bpf-path", "unused.o",
			}
			if tt.flag != "" {
				args = append(args, tt.flag)
			}

			if err := app.Run(args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if isTLPEnabled != tt.want {
				t.Fatalf("TLP enabled = %t, want %t", isTLPEnabled, tt.want)
			}
		})
	}
}

func TestAppRateLimitFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want uint64
	}{
		{
			name: "disabled by default",
		},
		{
			name: "explicit limit",
			args: []string{"--max-events-per-second", "100"},
			want: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var maxEventsPerSecond uint64
			app := newTestApp(func(c *cli.Context) error {
				maxEventsPerSecond = c.Uint64(cliFlagMaxEventsPerSecond)
				return nil
			})
			args := []string{
				"tcpshark", "--mode", "retransmit", "--bpf-path", "unused.o",
			}
			args = append(args, tt.args...)

			if err := app.Run(args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if maxEventsPerSecond != tt.want {
				t.Fatalf("max events/sec = %d, want %d", maxEventsPerSecond, tt.want)
			}
		})
	}
}

func TestAppSourceTypes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "tools by default", want: toolstream.SourceTypeTool},
		{
			name: "events",
			args: []string{"--source-types", toolstream.SourceTypeEvent},
			want: toolstream.SourceTypeEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sourceType string
			app := newTestApp(func(c *cli.Context) error {
				sourceType = c.String(cliFlagSourceTypes)
				return nil
			})
			args := []string{"tcpshark", "--mode", "retransmit", "--bpf-path", "unused.o"}
			args = append(args, tt.args...)

			if err := app.Run(args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if sourceType != tt.want {
				t.Fatalf("source type = %q, want %q", sourceType, tt.want)
			}
		})
	}
}

func TestAppHelpHidesSourceTypes(t *testing.T) {
	var output bytes.Buffer
	app := newTestApp(func(_ *cli.Context) error { return nil })
	app.Writer = &output

	if err := app.Run([]string{"tcpshark", "--help"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(output.String(), cliFlagSourceTypes) {
		t.Fatalf("help output contains hidden flag %q", cliFlagSourceTypes)
	}
}

func TestAppRejectsInvalidFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{
			name:      "negative duration",
			args:      []string{"--duration", "-1"},
			wantError: "invalid --duration -1",
		},
		{
			name:      "task id without storage",
			args:      []string{"--task-id", "task-1"},
			wantError: "--task-id requires --output-storage",
		},
		{
			name:      "unknown source type",
			args:      []string{"--source-types", "unknown"},
			wantError: `invalid --source-types "unknown"`,
		},
		{
			name:      "unknown output format",
			args:      []string{"--output", "yaml"},
			wantError: `invalid --output "yaml"`,
		},
		{
			name:      "positional argument",
			args:      []string{"unexpected"},
			wantError: "unexpected arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{
				"tcpshark", "--mode", "retransmit", "--bpf-path", "unused.o",
			}
			args = append(args, tt.args...)
			err := newTestApp(func(_ *cli.Context) error { return nil }).Run(args)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestAppWritesOutputStorageWarningToStderr(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newTestApp(func(_ *cli.Context) error { return nil })
	app.Writer = &stdout
	app.ErrWriter = &stderr

	err := app.Run([]string{
		"tcpshark",
		"--mode", "retransmit",
		"--bpf-path", "unused.o",
		"--output", "json",
		"--output-storage", "/tmp/unused.sock",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "warning: --output is ignored because --output-storage is set\n" {
		t.Fatalf("stderr = %q, want output warning", got)
	}
}

func newTestApp(action cli.ActionFunc) *cli.App {
	return &cli.App{
		Name:      tcpSharkToolName,
		Flags:     appFlags(),
		Action:    action,
		Before:    validateFlags,
		Writer:    io.Discard,
		ErrWriter: io.Discard,
	}
}
