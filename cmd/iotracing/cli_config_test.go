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
	"flag"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestLoadConfigRejectsNonPositiveDisplayLimits(t *testing.T) {
	tests := []struct {
		flagName string
		value    string
	}{
		{flagName: cliFlagMaxStack, value: "0"},
		{flagName: cliFlagMaxStack, value: "-1"},
		{flagName: cliFlagMaxProcess, value: "0"},
		{flagName: cliFlagMaxProcess, value: "-1"},
		{flagName: cliFlagMaxFilesPerPid, value: "0"},
		{flagName: cliFlagMaxFilesPerPid, value: "-1"},
	}

	for _, test := range tests {
		t.Run(test.flagName+"="+test.value, func(t *testing.T) {
			ctx := newTestCLIContext(t, "--"+test.flagName+"="+test.value)
			_, _, err := loadConfig(ctx)
			if err == nil {
				t.Fatalf("loadConfig() error = nil, want %s validation error", test.flagName)
			}
			if !strings.Contains(err.Error(), "--"+test.flagName) {
				t.Fatalf("loadConfig() error = %q, want flag name %q", err, test.flagName)
			}
		})
	}
}

func TestLoadConfigAcceptsPositiveDisplayLimits(t *testing.T) {
	ctx := newTestCLIContext(
		t,
		"--"+cliFlagMaxStack+"=1",
		"--"+cliFlagMaxProcess+"=2",
		"--"+cliFlagMaxFilesPerPid+"=3",
	)

	cfg, _, err := loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.maxStack != 1 || cfg.maxProcess != 2 || cfg.maxFilesPerProcess != 3 {
		t.Fatalf(
			"display limits = (%d, %d, %d), want (1, 2, 3)",
			cfg.maxStack,
			cfg.maxProcess,
			cfg.maxFilesPerProcess,
		)
	}
}

func newTestCLIContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	for _, cliFlag := range appFlags() {
		if err := cliFlag.Apply(set); err != nil {
			t.Fatalf("apply flag: %v", err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	return cli.NewContext(cli.NewApp(), set, nil)
}
