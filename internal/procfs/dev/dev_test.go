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

package dev

import (
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/procfs"

	"github.com/stretchr/testify/assert"
)

func TestDefaultPath(t *testing.T) {
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(DefaultPath())

	wantedPath := filepath.Join(tmpRoot, "dev")

	procfs.RootPrefix(tmpRoot)
	defer procfs.RootPrefix(originalPrefix)

	got := DefaultPath()
	assert.Equal(t, wantedPath, got)
}

func TestPath(t *testing.T) {
	tempRoot := t.TempDir()
	originalPrefix := filepath.Dir(DefaultPath())

	procfs.RootPrefix(tempRoot)
	defer procfs.RootPrefix(originalPrefix)

	wantedBase := filepath.Join(tempRoot, "/dev")
	assert.Equal(t, wantedBase, Path(""))

	wantedPath := filepath.Join(wantedBase, "deva", "devb")
	assert.Equal(t, wantedPath, Path("deva", "devb"))
}
