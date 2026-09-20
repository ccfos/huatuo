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

package java

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJavaDiscovery(t *testing.T) {
	root := t.TempDir()
	if _, err := discoverVM(t.Context(), root, 1); err == nil || errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("missing maps must be an inspection failure: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "1/maps"), []byte("1000-2000 r-xp 00000000 00:00 0 /bin/native\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverVM(t.Context(), root, 1); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("non-HotSpot mapping must be unsupported: %v", err)
	}
}

func TestJavaReleaseBounds(t *testing.T) {
	version, err := parseJavaRelease(t.Context(), strings.NewReader("JAVA_VERSION=\"17.0.12\"\n"))
	if err != nil || version != "17.0.12" {
		t.Fatalf("release: %q, %v", version, err)
	}
	if _, err := parseJavaRelease(t.Context(), strings.NewReader(strings.Repeat("X=1\n", 257))); err == nil {
		t.Fatal("unbounded release metadata")
	}
}
