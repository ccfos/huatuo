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

package symbol

import (
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/procfs"
)

func setupTempProcRoot(t *testing.T) string {
	t.Helper()
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return tmpRoot
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func bytesFramesToStrings(frames [][]byte) []string {
	result := make([]string, 0, len(frames))
	for _, frame := range frames {
		result = append(result, string(frame))
	}
	return result
}
