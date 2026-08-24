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

package collector

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNetStatFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "netstat")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestParseNetStatBlankLineNoPanic(t *testing.T) {
	// A blank line makes the first field empty; slicing off the trailing
	// ":" used to panic with index out of range before the guard.
	path := writeNetStatFile(t, "Tcp: ActiveOpens PassiveOpens\nTcp: 1 2\n\n")

	stats, err := parseNetStat(path)
	if err != nil {
		t.Fatalf("parseNetStat: %v", err)
	}
	if got := stats["Tcp"]["ActiveOpens"]; got != "1" {
		t.Fatalf("Tcp.ActiveOpens = %q, want %q", got, "1")
	}
}

func TestParseNetStatMissingValueLine(t *testing.T) {
	// A header with no following value line must return an error instead of
	// pairing the header with stale scanner text.
	path := writeNetStatFile(t, "Tcp: ActiveOpens PassiveOpens\n")

	if _, err := parseNetStat(path); err == nil {
		t.Fatal("parseNetStat: expected error for missing value line, got nil")
	}
}

func TestParseNetStatScannerError(t *testing.T) {
	// A value line longer than bufio.MaxScanTokenSize makes the inner Scan
	// fail; that scanner error must be surfaced instead of the misleading
	// missing-value-line error.
	content := "Tcp: ActiveOpens\n" + strings.Repeat("A", bufio.MaxScanTokenSize+1)
	path := writeNetStatFile(t, content)

	_, err := parseNetStat(path)
	if err == nil {
		t.Fatal("parseNetStat: expected scanner error, got nil")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("parseNetStat: error = %v, want bufio.ErrTooLong", err)
	}
}
