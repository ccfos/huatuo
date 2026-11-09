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
	"errors"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/runmode"
)

func TestConfiguredRuntimeMode(t *testing.T) {
	for _, mode := range []string{"none", "baremetal", "sandbox", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), "mode.conf", "Mode = \""+mode+"\"\n")
			err := Load(path)
			if mode == "unsupported" {
				if err == nil {
					t.Error("unsupported mode accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if Get().Mode != runmode.Mode(mode) {
				t.Errorf("mode=%q", Get().Mode)
			}
			if err := Update(map[string]any{"Mode": runmode.Mode("none")}); mode != "none" && !errors.Is(err, ErrInvalidUpdate) {
				t.Errorf("runtime mode changed without restart: %v", err)
			}
		})
	}
	path := writeConfigFile(t, t.TempDir(), "default.conf", "")
	if err := Load(filepath.Clean(path)); err != nil {
		t.Fatal(err)
	}
}
