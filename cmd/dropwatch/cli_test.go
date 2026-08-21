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

func TestAppSourceTypes(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			var sourceTypes string
			app := &cli.App{
				Name: dropwatchToolName, Flags: appFlags(), Before: validateFlags,
				Writer: io.Discard, ErrWriter: io.Discard,
				Action: func(c *cli.Context) error {
					sourceTypes = c.String(cliFlagSourceTypes)
					return nil
				},
			}
			args := []string{"dropwatch", "--bpf-path", "unused.o"}
			args = append(args, tt.args...)

			if err := app.Run(args); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if sourceTypes != tt.want {
				t.Fatalf("source types = %q, want %q", sourceTypes, tt.want)
			}
		})
	}
}

func TestAppHelpHidesSourceTypes(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	app := &cli.App{
		Name: dropwatchToolName, Flags: appFlags(), Before: validateFlags,
		Action: func(_ *cli.Context) error { return nil },
		Writer: io.Discard, ErrWriter: io.Discard,
	}
	app.Writer = &output

	if err := app.Run([]string{"dropwatch", "--help"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(output.String(), cliFlagSourceTypes) {
		t.Fatalf("help output contains hidden flag %q", cliFlagSourceTypes)
	}
}

func TestValidateFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "negative duration", args: []string{"--duration", "-1"}, want: "invalid --duration"},
		{name: "duration overflow", args: []string{"--duration", "9223372037"}, want: "invalid --duration"},
		{name: "task ID without storage", args: []string{"--task-id", "task"}, want: "--task-id requires --output-storage"},
		{name: "mutually exclusive devices", args: []string{"--device", "eth0", "--device-excluded", "eth1"}, want: "mutually exclusive"},
		{name: "invalid source type", args: []string{"--source-types", "invalid"}, want: "invalid --source-types"},
		{name: "unexpected argument", args: []string{"extra"}, want: "unexpected arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &cli.App{
				Name: dropwatchToolName, Flags: appFlags(), Before: validateFlags,
				Action: func(_ *cli.Context) error { return nil },
				Writer: io.Discard, ErrWriter: io.Discard,
			}
			err := app.Run(append([]string{"dropwatch", "--bpf-path", "unused.o"}, tt.args...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
