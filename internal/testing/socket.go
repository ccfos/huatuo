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

package testutils

import (
	"os"
	"path/filepath"
	"testing"
)

// SocketPath allocates an isolated Unix socket path below Linux's path limit.
// Use /tmp directly because TMPDIR and testing.TB.TempDir may be too long.
func SocketPath(tb testing.TB) string {
	tb.Helper()

	dir, err := os.MkdirTemp("/tmp", "huatuo-socket-")
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			tb.Errorf("remove socket directory: %v", err)
		}
	})

	return filepath.Join(dir, "events.sock")
}
