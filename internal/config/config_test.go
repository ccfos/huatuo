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
		t.Errorf("write config file %s: %v", path, err)
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
					t.Errorf("Load returned error: %v", err)
					return
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
					t.Errorf("Load returned error: %v", err)
					return
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
		t.Errorf("Sync returned error: %v", err)
		return
	}

	Set(cfg, "Name", "huatuo-region")
	Set(cfg, "Nested.Value", "kernel_sched_tick")

	if err := Sync(path, cfg); err != nil {
		t.Errorf("Sync after Set returned error: %v", err)
		return
	}

	reloaded := &sampleConfig{}
	if err := Load(path, reloaded); err != nil {
		t.Errorf("Load after Sync returned error: %v", err)
		return
	}

	if reloaded.Name != "huatuo-region" {
		t.Errorf("unexpected reloaded Name: %q", reloaded.Name)
	}
	if reloaded.Nested.Value != "kernel_sched_tick" {
		t.Errorf("unexpected reloaded nested value: %q", reloaded.Nested.Value)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read synced file: %v", err)
		return
	}
	if !strings.Contains(string(raw), "name = \"huatuo-region\"") {
		t.Errorf("synced file should contain updated name, got: %s", string(raw))
	}
}
