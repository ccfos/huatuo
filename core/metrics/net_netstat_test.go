// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"os"
	"path/filepath"
	"testing"
)

// A header line without its value row must stop the parse instead of
// re-reading the header as the values (which would surface as a ParseFloat
// error on every metric name) or panicking on a blank first field.
func TestParseNetStatStopsAtHeaderWithoutValueRow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "netstat")
	// A well-formed file would have a value row; this one ends after the
	// header, as a truncated or mid-write snapshot can.
	if err := os.WriteFile(file, []byte("Tcp: RtoAlgorithm RtoMin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := parseNetStat(file)
	if err != nil {
		t.Fatalf("parse truncated netstat: %v", err)
	}
	// The truncated header contributes nothing; the collector skips rather
	// than producing wrong values.
	if got := len(stats["Tcp"]); got != 0 {
		t.Fatalf("truncated header produced %d metrics, want 0", got)
	}
}

// A blank line must not panic on the trailing-colon strip.
func TestParseNetStatSkipsBlankLineWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("parse panicked on blank line: %v", r)
		}
	}()

	dir := t.TempDir()
	file := filepath.Join(dir, "netstat")
	if err := os.WriteFile(file, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := parseNetStat(file); err != nil {
		t.Fatalf("parse blank netstat: %v", err)
	}
}

// The well-formed two-row shape still parses.
func TestParseNetStatParsesWellFormedRows(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "netstat")
	content := "Tcp: RtoAlgorithm RtoMin\nTcp: 1 200\n" +
		"TcpExt: SyncookiesSent\nTcpExt: 0\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := parseNetStat(file)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := stats["Tcp"]["RtoAlgorithm"]; got != "1" {
		t.Fatalf("Tcp RtoAlgorithm = %q, want %q", got, "1")
	}
	if got := stats["Tcp"]["RtoMin"]; got != "200" {
		t.Fatalf("Tcp RtoMin = %q, want %q", got, "200")
	}
	if got := stats["TcpExt"]["SyncookiesSent"]; got != "0" {
		t.Fatalf("TcpExt SyncookiesSent = %q, want %q", got, "0")
	}
}
