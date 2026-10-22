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
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/config"
	"github.com/ccfos/huatuo/internal/log"

	"github.com/urfave/cli/v2"
)

func TestOptionsFromContextPreservesExplicitRelativeConfigDir(t *testing.T) {
	app := cli.NewApp()
	opts := &Options{}
	opts.AddFlags(app)
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, cliFlag := range app.Flags {
		if err := cliFlag.Apply(flags); err != nil {
			t.Fatalf("apply flag: %v", err)
		}
	}
	if err := flags.Parse([]string{
		"--config-dir", "relative-conf",
		"--enable-mcp",
		"--enable-pprof",
		"--enable-cgroup",
		"--log-debug",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	if err := opts.FromContext(cli.NewContext(app, flags, nil)); err != nil {
		t.Fatalf("FromContext() error = %v", err)
	}
	if opts.ConfigDir != "relative-conf" {
		t.Errorf("ConfigDir = %q, want %q", opts.ConfigDir, "relative-conf")
	}
	if !opts.EnableMCP {
		t.Error("EnableMCP = false, want true")
	}
	if !opts.EnablePProf {
		t.Error("EnablePProf = false, want true")
	}
	if !opts.EnableCgroup {
		t.Error("EnableCgroup = false, want true")
	}
	if !opts.LogDebug {
		t.Error("LogDebug = false, want true")
	}
}

func TestMCPEnabled(t *testing.T) {
	tests := []struct {
		name    string
		cli     bool
		config  bool
		enabled bool
	}{
		{name: "disabled"},
		{name: "CLI", cli: true, enabled: true},
		{name: "config", config: true, enabled: true},
		{name: "both", cli: true, config: true, enabled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := &Options{EnableMCP: test.cli, Config: &config.Config{}}
			opts.Config.MCP.Enabled = test.config
			if got := mcpEnabled(opts); got != test.enabled {
				t.Fatalf("mcpEnabled() = %t, want %t", got, test.enabled)
			}
		})
	}
}

func TestEnableMCPEnvironmentVariable(t *testing.T) {
	t.Setenv("ENABLE_MCP", "true")
	app := cli.NewApp()
	opts := &Options{}
	opts.AddFlags(app)
	app.Action = func(ctx *cli.Context) error {
		return opts.FromContext(ctx)
	}
	if err := app.Run([]string{"huatuo-apiserver"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !opts.EnableMCP {
		t.Fatal("EnableMCP = false, want true from ENABLE_MCP")
	}
}

func TestConfigureRuntimeAnchorsRelativeJobStoreToConfigDirectory(t *testing.T) {
	configDir := t.TempDir()
	configFile := "apiserver.conf"
	contents := []byte(`
[[Auth.Users]]
ID = "test-user"
BearerToken = "test-token"
Admin = true

[Agent.Auth]
BearerToken = "node-token"

[Jobs]
StoreDSN = "state/jobs.db"
`)
	if err := os.WriteFile(filepath.Join(configDir, configFile), contents, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	opts := &Options{ConfigDir: configDir, ConfigFile: configFile}
	if err := configureRuntime(opts); err != nil {
		t.Fatalf("configureRuntime() error = %v", err)
	}
	want := filepath.Join(configDir, "state/jobs.db")
	if got := opts.Config.Jobs.StoreDSN; got != want {
		t.Fatalf("StoreDSN = %q, want %q", got, want)
	}
}

func TestConfigureRuntimeLogDebugOverridesConfigLevel(t *testing.T) {
	originalLevel := log.GetLevel()
	t.Cleanup(func() {
		log.SetLevel(originalLevel.String())
	})

	configDir := t.TempDir()
	configFile := "apiserver.conf"
	contents := []byte(`
[Log]
Level = "Error"

[[Auth.Users]]
ID = "test-user"
BearerToken = "test-token"
Admin = true

[Agent.Auth]
BearerToken = "node-token"
`)
	if err := os.WriteFile(filepath.Join(configDir, configFile), contents, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	opts := &Options{
		ConfigDir:  configDir,
		ConfigFile: configFile,
		LogDebug:   true,
	}
	if err := configureRuntime(opts); err != nil {
		t.Fatalf("configureRuntime() error = %v", err)
	}
	if got := log.GetLevel().String(); got != "debug" {
		t.Fatalf("log level = %q, want %q", got, "debug")
	}
}
