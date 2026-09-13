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
	"errors"
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
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod synced config: %v", err)
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat synced file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("synced file mode = %o, want 640", got)
	}
	temps, err := filepath.Glob(filepath.Join(tmpDir, ".sync-config.toml.tmp-*"))
	if err != nil {
		t.Fatalf("find temporary config files: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary config files remain after success: %v", temps)
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

type fakeSyncFile struct {
	name   string
	events *[]string
	wrote  bool
	closed bool
}

func (f *fakeSyncFile) Write(p []byte) (int, error) {
	if !f.wrote {
		*f.events = append(*f.events, "write")
		f.wrote = true
	}
	return len(p), nil
}

func (f *fakeSyncFile) Name() string {
	return f.name
}

func (f *fakeSyncFile) Chmod(os.FileMode) error {
	*f.events = append(*f.events, "chmod")
	return nil
}

func (f *fakeSyncFile) Sync() error {
	*f.events = append(*f.events, "file-sync")
	return nil
}

func (f *fakeSyncFile) Close() error {
	if f.closed {
		return os.ErrClosed
	}
	f.closed = true
	*f.events = append(*f.events, "file-close")
	return nil
}

type fakeSyncDirectory struct {
	events   *[]string
	syncErr  error
	closeErr error
}

func (d *fakeSyncDirectory) Sync() error {
	*d.events = append(*d.events, "directory-sync")
	return d.syncErr
}

func (d *fakeSyncDirectory) Close() error {
	*d.events = append(*d.events, "directory-close")
	return d.closeErr
}

func TestSyncFlushesDirectoryAfterRename(t *testing.T) {
	var events []string
	const tempPath = "/config/.huatuo.toml.tmp-test"
	file := &fakeSyncFile{name: tempPath, events: &events}
	directory := &fakeSyncDirectory{events: &events}
	operations := syncOperations{
		createTemp: func(dir, pattern string) (syncFile, error) {
			if dir != "/config" {
				t.Fatalf("temporary directory = %q, want /config", dir)
			}
			if pattern != ".huatuo.toml.tmp-*" {
				t.Fatalf("temporary pattern = %q", pattern)
			}
			events = append(events, "create")
			return file, nil
		},
		stat: func(string) (os.FileInfo, error) {
			events = append(events, "stat")
			return nil, os.ErrNotExist
		},
		rename: func(oldPath, newPath string) error {
			if oldPath != tempPath || newPath != "/config/huatuo.toml" {
				t.Fatalf("rename(%q, %q)", oldPath, newPath)
			}
			events = append(events, "rename")
			return nil
		},
		remove: func(path string) error {
			t.Fatalf("unexpected remove(%q)", path)
			return nil
		},
		openDirectory: func(path string) (syncDirectory, error) {
			if path != "/config" {
				t.Fatalf("directory path = %q, want /config", path)
			}
			events = append(events, "directory-open")
			return directory, nil
		},
	}

	if err := syncConfig("/config/huatuo.toml", sampleConfig{}, operations); err != nil {
		t.Fatalf("syncConfig() error = %v", err)
	}
	want := []string{
		"create",
		"stat",
		"chmod",
		"write",
		"file-sync",
		"file-close",
		"rename",
		"directory-open",
		"directory-sync",
		"directory-close",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("operation order = %v, want %v", events, want)
	}
}

func TestSyncReturnsDirectoryDurabilityErrors(t *testing.T) {
	openErr := errors.New("directory open failed")
	syncErr := errors.New("directory sync failed")
	closeErr := errors.New("directory close failed")
	tests := []struct {
		name      string
		openErr   error
		syncErr   error
		closeErr  error
		wantError []error
	}{
		{name: "open", openErr: openErr, wantError: []error{openErr}},
		{name: "sync", syncErr: syncErr, wantError: []error{syncErr}},
		{name: "close", closeErr: closeErr, wantError: []error{closeErr}},
		{
			name:      "sync and close",
			syncErr:   syncErr,
			closeErr:  closeErr,
			wantError: []error{syncErr, closeErr},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			const tempPath = "/config/.huatuo.toml.tmp-test"
			file := &fakeSyncFile{name: tempPath, events: &events}
			operations := syncOperations{
				createTemp: func(string, string) (syncFile, error) {
					return file, nil
				},
				stat: func(string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				},
				rename: func(string, string) error {
					events = append(events, "rename")
					return nil
				},
				remove: func(path string) error {
					if path != tempPath {
						t.Fatalf("remove path = %q, want %q", path, tempPath)
					}
					events = append(events, "remove-temp")
					return os.ErrNotExist
				},
				openDirectory: func(string) (syncDirectory, error) {
					events = append(events, "directory-open")
					if tt.openErr != nil {
						return nil, tt.openErr
					}
					return &fakeSyncDirectory{
						events:   &events,
						syncErr:  tt.syncErr,
						closeErr: tt.closeErr,
					}, nil
				},
			}

			err := syncConfig("/config/huatuo.toml", sampleConfig{}, operations)
			if err == nil {
				t.Fatal("syncConfig() error = nil, want durability error")
			}
			for _, wantErr := range tt.wantError {
				if !errors.Is(err, wantErr) {
					t.Fatalf("syncConfig() error = %v, want errors.Is(%v)", err, wantErr)
				}
			}
			if !strings.Contains(strings.Join(events, ","), "rename,directory-open") {
				t.Fatalf("directory was not opened immediately after rename: %v", events)
			}
			if events[len(events)-1] != "remove-temp" {
				t.Fatalf("temporary cleanup was not last: %v", events)
			}
		})
	}
}

func TestSyncSkipsDirectorySyncAfterRenameFailure(t *testing.T) {
	var events []string
	const tempPath = "/config/.huatuo.toml.tmp-test"
	renameErr := errors.New("rename failed")
	file := &fakeSyncFile{name: tempPath, events: &events}
	operations := syncOperations{
		createTemp: func(string, string) (syncFile, error) { return file, nil },
		stat:       func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		rename: func(string, string) error {
			events = append(events, "rename")
			return renameErr
		},
		remove: func(path string) error {
			if path != tempPath {
				t.Fatalf("remove path = %q, want %q", path, tempPath)
			}
			events = append(events, "remove-temp")
			return nil
		},
		openDirectory: func(string) (syncDirectory, error) {
			t.Fatal("directory must not be opened after rename failure")
			return nil, nil
		},
	}

	err := syncConfig("/config/huatuo.toml", sampleConfig{}, operations)
	if !errors.Is(err, renameErr) {
		t.Fatalf("syncConfig() error = %v, want rename error", err)
	}
	if events[len(events)-1] != "remove-temp" {
		t.Fatalf("temporary file not removed after rename failure: %v", events)
	}
}
