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
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestCLIRejectsUnusedInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "task without storage", args: []string{"--task-id", "job-42"}, want: "--task-id requires --output-storage"},
		{name: "unexpected argument", args: []string{"extra"}, want: "unexpected arguments"},
		{name: "local output"},
		{name: "storage task", args: []string{"--output-storage", "/tmp/huatuo.sock", "--task-id", "job-42"}},
		{name: "storage without optional task", args: []string{"--output-storage", "/tmp/huatuo.sock"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := cli.NewApp()
			app.Flags = appFlags()
			app.Action = validateFlags
			err := app.Run(append([]string{"iotracing", "--bpf-path", "unused.o"}, tt.args...))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %q", err, tt.want)
			}
		})
	}
}
