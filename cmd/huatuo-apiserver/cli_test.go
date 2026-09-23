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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

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

func TestConfigureRuntimeAnchorsRelativeSQLiteURI(t *testing.T) {
	configDir := t.TempDir()
	tests := []struct {
		name      string
		dsn       string
		wantFile  string
		wantQuery string
	}{
		{
			name:      "relative file with query",
			dsn:       "file:jobs.db?mode=rwc",
			wantFile:  filepath.Join(configDir, "jobs.db"),
			wantQuery: "mode=rwc",
		},
		{
			name:      "relative path with query",
			dsn:       "file:state/jobs.db?mode=rwc&cache=shared",
			wantFile:  filepath.Join(configDir, "state/jobs.db"),
			wantQuery: "mode=rwc&cache=shared",
		},
		{
			name:      "escaped filename",
			dsn:       "file:state/job%20store.db?mode=rwc",
			wantFile:  filepath.Join(configDir, "state/job store.db"),
			wantQuery: "mode=rwc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := configureRuntimeWithJobDSN(t, configDir, tt.dsn)
			got, err := url.Parse(opts.Config.Jobs.StoreDSN)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if got.Scheme != "file" || got.Path != filepath.ToSlash(tt.wantFile) ||
				got.RawQuery != tt.wantQuery {
				t.Fatalf("StoreDSN = %q, want file path %q and query %q",
					opts.Config.Jobs.StoreDSN, tt.wantFile, tt.wantQuery)
			}
		})
	}
}

func TestConfigureRuntimePreservesSpecialSQLiteURIs(t *testing.T) {
	configDir := t.TempDir()
	for _, dsn := range []string{
		"file:/var/lib/jobs.db?mode=ro",
		"file:///var/lib/jobs.db?mode=ro",
		"file::memory:?cache=shared",
		"file:?cache=shared",
		"file:?mode=memory&cache=shared",
		"file:jobs.db?mode=memory&cache=shared",
	} {
		t.Run(dsn, func(t *testing.T) {
			opts := configureRuntimeWithJobDSN(t, configDir, dsn)
			if got := opts.Config.Jobs.StoreDSN; got != dsn {
				t.Fatalf("StoreDSN = %q, want %q", got, dsn)
			}
		})
	}
}

func configureRuntimeWithJobDSN(t *testing.T, configDir, dsn string) *Options {
	t.Helper()
	configFile := "apiserver.conf"
	contents := fmt.Sprintf(`
[[Auth.Users]]
ID = "test-user"
BearerToken = "test-token"
Admin = true

[Agent.Auth]
BearerToken = "node-token"

[Jobs]
StoreDSN = %q
`, dsn)
	if err := os.WriteFile(filepath.Join(configDir, configFile), []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	opts := &Options{ConfigDir: configDir, ConfigFile: configFile}
	if err := configureRuntime(opts); err != nil {
		t.Fatalf("configureRuntime() error = %v", err)
	}
	return opts
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
