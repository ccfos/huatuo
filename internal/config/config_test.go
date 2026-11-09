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

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sampleConfig struct {
	Name    string `toml:"name"`
	Count   int    `toml:"count"`
	Enabled bool   `toml:"enabled"`
	Nested  struct {
		Value string `toml:"value"`
	} `toml:"nested"`
}

func writeConfigFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file %s: %v", path, err)
		return ""
	}

	return path
}

// mustSymlink creates link pointing at target, failing the test on error.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink %s -> %s: %v", link, target, err)
	}
}

// linkText returns how a link stores target: verbatim when absolute, or as a
// bare base name because every test link lives beside its target.
func linkText(target string, absolute bool) string {
	if absolute {
		return target
	}
	return filepath.Base(target)
}

// configTempFiles lists the atomic-write scratch files left in dir.
func configTempFiles(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil {
		t.Fatalf("find temporary files: %v", err)
	}
	return matches
}

func TestCoreBinDir(t *testing.T) {
	if CoreBinDir == "" {
		t.Errorf("CoreBinDir should not be empty")
	}
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()

	cases := []struct {
		name     string
		content  string
		validate func(*testing.T, error, *sampleConfig)
	}{
		{
			name: "valid-basic-config",
			content: `
name = "huatuo-dev"
count = 8
enabled = true
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err != nil {
					t.Fatalf("Load returned error: %v", err)
				}
				if cfg.Name != "huatuo-dev" {
					t.Errorf("unexpected Name: %q", cfg.Name)
				}
				if cfg.Count != 8 {
					t.Errorf("unexpected Count: %d", cfg.Count)
				}
				if !cfg.Enabled {
					t.Errorf("Enabled should be true")
				}
			},
		},
		{
			name: "valid-nested-config",
			content: `
name = "huatuo-region"
count = 12
enabled = false

[nested]
value = "kernel_sched_tick"
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err != nil {
					t.Fatalf("Load returned error: %v", err)
				}
				if cfg.Nested.Value != "kernel_sched_tick" {
					t.Errorf("unexpected nested value: %q", cfg.Nested.Value)
				}
			},
		},
		{
			name: "type-mismatch",
			content: `
name = "huatuo-dev"
count = "unexpected-string"
enabled = true
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err == nil {
					t.Errorf("Load should fail for type mismatch")
				}
			},
		},
		{
			name: "unknown-field",
			content: `
name = "huatuo-dev"
count = 8
enabled = true
unexpected_key = "strict-mode"
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err == nil {
					t.Errorf("Load should fail for unknown field")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tmpDir, tc.name+".toml", tc.content)
			if path == "" {
				return
			}

			cfg := &sampleConfig{}
			err := Load(path, cfg)
			tc.validate(t, err, cfg)
		})
	}
}

func TestSyncAndSet(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sync-config.toml")

	cfg := &sampleConfig{
		Name:    "huatuo-dev",
		Count:   5,
		Enabled: true,
	}
	cfg.Nested.Value = "trace-2026"

	if err := Sync(path, cfg); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	if err := Set(cfg, "Name", "huatuo-region"); err != nil {
		t.Fatalf("Set Name returned error: %v", err)
	}
	if err := Set(cfg, "Nested.Value", "kernel_sched_tick"); err != nil {
		t.Fatalf("Set Nested.Value returned error: %v", err)
	}

	if err := Sync(path, cfg); err != nil {
		t.Fatalf("Sync after Set returned error: %v", err)
	}

	reloaded := &sampleConfig{}
	if err := Load(path, reloaded); err != nil {
		t.Fatalf("Load after Sync returned error: %v", err)
	}

	if reloaded.Name != "huatuo-region" {
		t.Errorf("unexpected reloaded Name: %q", reloaded.Name)
	}
	if reloaded.Nested.Value != "kernel_sched_tick" {
		t.Errorf("unexpected reloaded nested value: %q", reloaded.Nested.Value)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read synced file: %v", err)
	}
	if !strings.Contains(string(raw), "name = \"huatuo-region\"") {
		t.Errorf("synced file should contain updated name, got: %s", string(raw))
	}
}

func TestSetRejectsInvalidAssignments(t *testing.T) {
	cfg := &sampleConfig{}
	tests := []struct {
		name string
		dst  any
		key  string
		val  any
	}{
		{name: "non-pointer", dst: sampleConfig{}, key: "Count", val: 1},
		{name: "nil pointer", dst: (*sampleConfig)(nil), key: "Count", val: 1},
		{name: "unknown field", dst: cfg, key: "Missing", val: 1},
		{name: "wrong type", dst: cfg, key: "Count", val: "1"},
		{name: "fractional integer", dst: cfg, key: "Count", val: json.RawMessage("1.5")},
		{name: "integer overflow", dst: cfg, key: "Count", val: json.RawMessage("18446744073709551616")},
		{name: "null", dst: cfg, key: "Name", val: json.RawMessage("null")},
		{
			name: "unknown nested field",
			dst:  cfg,
			key:  "Nested",
			val:  json.RawMessage(`{"Unknown":"value"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Set(tt.dst, tt.key, tt.val); err == nil {
				t.Fatal("Set() error = nil, want invalid assignment error")
			}
		})
	}
}

func TestSetDecodesJSONValue(t *testing.T) {
	cfg := &sampleConfig{}
	if err := Set(cfg, "Count", json.RawMessage("8")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if cfg.Count != 8 {
		t.Fatalf("Count = %d, want 8", cfg.Count)
	}
}

func TestSyncPreservesOriginalOnEncodeFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "config.toml", "name = \"original\"\n")
	unsupported := struct {
		Values chan int
	}{Values: make(chan int)}

	if err := Sync(path, unsupported); err == nil {
		t.Fatal("Sync() error = nil, want encoding error")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original config: %v", err)
	}
	if got := string(raw); got != "name = \"original\"\n" {
		t.Fatalf("config contents = %q, want original contents", got)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".config.toml.tmp-*"))
	if err != nil {
		t.Fatalf("find temporary files: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary config files remain: %v", temps)
	}
}

func TestSyncThroughDanglingSymbolicLink(t *testing.T) {
	tests := []struct {
		name     string
		absolute bool
	}{
		{name: "absolute-target", absolute: true},
		{name: "relative-target", absolute: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "missing.toml")
			link := filepath.Join(dir, "active.toml")
			mustSymlink(t, linkText(target, tt.absolute), link)

			cfg := &sampleConfig{Name: "bootstrap", Count: 1}
			if err := Sync(link, cfg); err != nil {
				t.Fatalf("Sync() error = %v, want target created", err)
			}

			info, err := os.Lstat(link)
			if err != nil {
				t.Fatalf("inspect link: %v", err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("Sync() replaced the configured symlink")
			}

			reloaded := &sampleConfig{}
			if err := Load(link, reloaded); err != nil {
				t.Fatalf("Load through link: %v", err)
			}
			if reloaded.Name != "bootstrap" || reloaded.Count != 1 {
				t.Fatalf("reloaded config = %+v, want bootstrap/1", reloaded)
			}

			if temps := configTempFiles(t, dir); len(temps) != 0 {
				t.Fatalf("temporary config files remain: %v", temps)
			}
		})
	}
}

func TestSyncCreatesDanglingSymbolicLinkTargetWithDefaults(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "active.toml")
	mustSymlink(t, "missing.toml", link)

	if err := Sync(link, &sampleConfig{Name: "fresh"}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatalf("stat created target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("created target mode = %o, want 600", got)
	}
}

func TestSyncThroughSymbolicLinkChain(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("name = \"old\"\n"), 0o400); err != nil {
		t.Fatalf("write target: %v", err)
	}

	middle := filepath.Join(dir, "middle.toml")
	link := filepath.Join(dir, "active.toml")
	mustSymlink(t, "config.toml", middle)
	mustSymlink(t, "middle.toml", link)

	if err := Sync(link, &sampleConfig{Name: "chained"}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	for _, path := range []string{link, middle} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("Sync() replaced symlink %s", path)
		}
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("target mode = %o, want 400", got)
	}

	reloaded := &sampleConfig{}
	if err := Load(target, reloaded); err != nil {
		t.Fatalf("load target: %v", err)
	}
	if reloaded.Name != "chained" {
		t.Fatalf("target name = %q, want chained", reloaded.Name)
	}
}

func TestSyncRejectsSymbolicLinkLoop(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "active.toml")
	other := filepath.Join(dir, "other.toml")
	mustSymlink(t, "other.toml", link)
	mustSymlink(t, "active.toml", other)

	err := Sync(link, &sampleConfig{Name: "loop"})
	if err == nil {
		t.Fatal("Sync() error = nil, want symbolic link loop error")
	}
	if !strings.Contains(err.Error(), link) {
		t.Fatalf("error %q does not name the config path %q", err, link)
	}

	if temps := configTempFiles(t, dir); len(temps) != 0 {
		t.Fatalf("temporary config files remain: %v", temps)
	}
}

func TestSyncRejectsSelfReferentialSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "active.toml")
	mustSymlink(t, "active.toml", link)

	err := Sync(link, &sampleConfig{Name: "self"})
	if err == nil {
		t.Fatal("Sync() error = nil, want symbolic link loop error")
	}
	if !strings.Contains(err.Error(), link) {
		t.Fatalf("error %q does not name the config path %q", err, link)
	}
}

func TestSyncKeepsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "config.toml", "name = \"plain\"\n")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod config: %v", err)
	}

	if err := Sync(path, &sampleConfig{Name: "updated"}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect config: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("config mode = %v, want regular file", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("config mode = %o, want 400", got)
	}

	reloaded := &sampleConfig{}
	if err := Load(path, reloaded); err != nil {
		t.Fatalf("load config: %v", err)
	}
	if reloaded.Name != "updated" {
		t.Fatalf("config name = %q, want updated", reloaded.Name)
	}
}
