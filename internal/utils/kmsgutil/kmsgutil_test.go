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

package kmsgutil

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func parseFormattedKmsgLine(line string) (time.Time, string, error) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return time.Time{}, "", errors.New("invalid formatted kmsg line")
	}

	ts, err := time.ParseInLocation("2006-01-02 15:04:05", parts[0]+" "+parts[1], time.Local)
	if err != nil {
		return time.Time{}, "", err
	}
	return ts, parts[2], nil
}

func TestFormatKmsgEntry(t *testing.T) {
	bootTime, err := getBootTime()
	if err != nil {
		t.Errorf("getBootTime() error=%v", err)
		return
	}

	tests := []struct {
		name     string
		entry    string
		validate func(*testing.T, string, error)
	}{
		{
			name:  "valid kmsg entry",
			entry: "6,1001,2026000;Test message",
			validate: func(t *testing.T, got string, err error) {
				if err != nil {
					t.Errorf("formatKmsgEntry() error=%v, want nil", err)
					return
				}

				ts, msg, parseErr := parseFormattedKmsgLine(got)
				if parseErr != nil {
					t.Errorf("parseFormattedKmsgLine(%q) error=%v", got, parseErr)
					return
				}
				if msg != "Test message" {
					t.Errorf("message=%q, want %q", msg, "Test message")
				}

				wantTime := bootTime.Add(2026000 * time.Microsecond)
				diff := ts.Sub(wantTime)
				if diff < 0 {
					diff = -diff
				}
				// allow small timing drift caused by separate boot-time reads in test and function.
				if diff > 2*time.Second {
					t.Errorf("timestamp diff=%v, want <= 2s (got=%v want~=%v)", diff, ts, wantTime)
				}
			},
		},
		{
			name:  "invalid format missing semicolon",
			entry: "6,1001",
			validate: func(t *testing.T, got string, err error) {
				if err == nil {
					t.Errorf("formatKmsgEntry() error=nil, want non-nil")
				}
				if got != "" {
					t.Errorf("formatKmsgEntry()=%q, want empty", got)
				}
			},
		},
		{
			name:  "invalid timestamp",
			entry: "6,1001,invalid_timestamp;Test message",
			validate: func(t *testing.T, got string, err error) {
				if err == nil {
					t.Errorf("formatKmsgEntry() error=nil, want non-nil")
				}
				if got != "" {
					t.Errorf("formatKmsgEntry()=%q, want empty", got)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			got, gotErr := formatKmsgEntry(tests[i].entry)
			tests[i].validate(t, got, gotErr)
		})
	}
}

func TestFormatKmsgs(t *testing.T) {
	tests := []struct {
		name     string
		kmsgs    string
		validate func(*testing.T, string)
	}{
		{
			name:  "multiple valid lines",
			kmsgs: "6,1001,2026000;Test message1\n6,1002,3026000;Test message2\n",
			validate: func(t *testing.T, got string) {
				lines := strings.Split(strings.TrimSpace(got), "\n")
				if len(lines) != 2 {
					t.Errorf("formatted line count=%d, want 2, got=%q", len(lines), got)
					return
				}
				if !strings.Contains(lines[0], "Test message1") {
					t.Errorf("line[0]=%q, want contains %q", lines[0], "Test message1")
				}
				if !strings.Contains(lines[1], "Test message2") {
					t.Errorf("line[1]=%q, want contains %q", lines[1], "Test message2")
				}
			},
		},
		{
			name:  "single valid line",
			kmsgs: "6,1001,2026000;Test message",
			validate: func(t *testing.T, got string) {
				lines := strings.Split(strings.TrimSpace(got), "\n")
				if len(lines) != 1 {
					t.Errorf("formatted line count=%d, want 1, got=%q", len(lines), got)
					return
				}
				if !strings.Contains(lines[0], "Test message") {
					t.Errorf("line[0]=%q, want contains %q", lines[0], "Test message")
				}
			},
		},
		{
			name:  "mixed valid and invalid lines",
			kmsgs: "6,1001,2026000;Test valid\ninvalid\n",
			validate: func(t *testing.T, got string) {
				lines := strings.Split(strings.TrimSpace(got), "\n")
				if len(lines) != 1 {
					t.Errorf("formatted line count=%d, want 1, got=%q", len(lines), got)
					return
				}
				if !strings.Contains(lines[0], "Test valid") {
					t.Errorf("line[0]=%q, want contains %q", lines[0], "Test valid")
				}
			},
		},
		{
			name:  "single invalid line",
			kmsgs: "invalid",
			validate: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("formatKmsgs()=%q, want empty", got)
				}
			},
		},
		{
			name:  "empty input",
			kmsgs: "",
			validate: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("formatKmsgs()=%q, want empty", got)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			tests[i].validate(t, formatKmsgs(tests[i].kmsgs))
		})
	}
}

func TestGetBootTime(t *testing.T) {
	bootTime, err := getBootTime()
	if err != nil {
		t.Errorf("getBootTime() error=%v", err)
		return
	}
	if bootTime.After(time.Now()) {
		t.Errorf("getBootTime() returned future time=%v", bootTime)
	}
}

// Note: GetSysrqMsg, GetAllCPUsBT, and GetBlockedProcessesBT involve system I/O (/dev/kmsg, /proc/sysrq-trigger)
// and are better suited for integration tests with mocked file systems (e.g., using afero or test containers).
// Unit tests for these would require dependency injection for os.Open, syscall.Read, etc., to isolate logic.
// For brevity, they are omitted here; focus on pure functions above.
