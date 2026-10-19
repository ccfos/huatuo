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
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestMockProfilingFlag(t *testing.T) {
	for _, tc := range []struct {
		name, typ, language, mode string
		mock, wantError           bool
	}{
		{"default", "memory", "c", "physical_alloc", false, false},
		{"physical alloc", "memory", "c", "physical_alloc", true, false},
		{"physical usage", "memory", "c", "physical_usage", true, false},
		{"virtual alloc", "memory", "c", "virtual_alloc", true, false},
		{"CPU", "cpu", "c", "", true, true},
		{"Java", "memory", "java", "object_alloc", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &cli.App{
				Flags: appFlags, Before: runBefore, After: closeLogging, Writer: io.Discard, ErrWriter: io.Discard,
				Action: func(ctx *cli.Context) error {
					cfg, err := loadConfig(ctx)
					if err != nil {
						return err
					}
					if cfg.BPFMock != tc.mock {
						return fmt.Errorf("unexpected mock configuration")
					}
					return nil
				},
			}
			args := []string{"profiler", "--type", tc.typ, "--language", tc.language, "--pid", fmt.Sprint(os.Getpid()), "--output-path", t.TempDir(), "--output-format", "collapsed"}
			if tc.mode != "" {
				args = append(args, "--memory-mode", tc.mode)
			}
			if tc.mock {
				args = append(args, "--bpf-mock")
			}
			if err := app.Run(args); (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
