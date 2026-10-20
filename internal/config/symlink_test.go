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
	"testing"
)

func TestSyncThroughSymlink(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "chain"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := writeConfigFile(t, dir, "target.toml", `name = "before"`)
			link := filepath.Join(dir, "active.toml")
			destination := target
			if kind != "absolute" {
				destination = "target.toml"
			}
			if kind == "chain" {
				if err := os.Symlink(destination, filepath.Join(dir, "intermediate.toml")); err != nil {
					t.Fatal(err)
				}
				destination = "intermediate.toml"
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			cfg := &sampleConfig{Name: "after", Count: 7}
			if err := Sync(link, cfg); err != nil {
				t.Fatal(err)
			}
			gotLink, err := os.Readlink(link)
			if err != nil || gotLink != destination {
				t.Fatalf("config link = %q, %v; want %q", gotLink, err, destination)
			}
			var stored sampleConfig
			if err := Load(target, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Name != cfg.Name || stored.Count != cfg.Count {
				t.Fatalf("target still contains %+v", stored)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("target permissions = %v, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestSyncRejectsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "active.toml")
	if err := os.Symlink("missing.toml", link); err != nil {
		t.Fatal(err)
	}
	if err := Sync(link, &sampleConfig{Name: "after"}); err == nil {
		t.Fatal("expected broken symlink error")
	}
	if target, err := os.Readlink(link); err != nil || target != "missing.toml" {
		t.Fatalf("broken link was replaced: %q, %v", target, err)
	}
}
