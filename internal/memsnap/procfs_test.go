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

package memsnap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessIdentity(t *testing.T) {
	procRoot := t.TempDir()
	pidDir := filepath.Join(procRoot, "42")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := []byte("42 (worker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 999")
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), stat, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := ProcessIdentity{TGID: 42, StartTimeTicks: 999}
	if err := ValidateIdentity(procRoot, identity); err != nil {
		t.Fatal(err)
	}
	identity.StartTimeTicks++
	if err := ValidateIdentity(procRoot, identity); err == nil {
		t.Fatal("changed process identity was accepted")
	}
}
