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
	"syscall"
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
		t.Fatalf("getBootTime() error=%v", err)
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
					t.Fatalf("formatKmsgEntry() error=%v, want nil", err)
				}

				ts, msg, parseErr := parseFormattedKmsgLine(got)
				if parseErr != nil {
					t.Fatalf("parseFormattedKmsgLine(%q) error=%v", got, parseErr)
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
					t.Fatalf("formatted line count=%d, want 2, got=%q", len(lines), got)
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
					t.Fatalf("formatted line count=%d, want 1, got=%q", len(lines), got)
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
					t.Fatalf("formatted line count=%d, want 1, got=%q", len(lines), got)
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
		t.Fatalf("getBootTime() error=%v", err)
	}
	if bootTime.After(time.Now()) {
		t.Errorf("getBootTime() returned future time=%v", bootTime)
	}
}

// Note: GetSysrqMsg, GetAllCPUsBT, and GetBlockedProcessesBT involve system I/O (/dev/kmsg, /proc/sysrq-trigger)
// and are better suited for integration tests with mocked file systems (e.g., using afero or test containers).
// Unit tests for these would require dependency injection for os.Open, syscall.Read, etc., to isolate logic.
// For brevity, they are omitted here; focus on pure functions above.

// scriptedKmsgRead replays a fixed sequence of (data, error) reads.
type scriptedKmsgRead struct {
	chunks [][]byte
	errs   []error
	index  int
	// seenBufLen records the buffer size passed on the first read, so a test
	// can assert the reader no longer uses a 1024-byte buffer.
	seenBufLen int
}

func (s *scriptedKmsgRead) read(buf []byte) (int, error) {
	if s.index == 0 {
		s.seenBufLen = len(buf)
	}
	if s.index >= len(s.chunks) {
		return 0, syscall.EAGAIN
	}
	data := s.chunks[s.index]
	err := error(nil)
	if s.index < len(s.errs) {
		err = s.errs[s.index]
	}
	s.index++
	n := copy(buf, data)
	return n, err
}

// TestDrainKmsgSkipsOversizedRecord verifies an EINVAL (oversized, already
// consumed record) is skipped instead of aborting the drain, so records
// accumulated before it survive.
func TestDrainKmsgSkipsOversizedRecord(t *testing.T) {
	read := &scriptedKmsgRead{
		chunks: [][]byte{
			[]byte("6,1,100,0;before\n"),
			nil, // oversized record consumed, read fails EINVAL
			[]byte("6,2,200,0;after\n"),
		},
		errs: []error{nil, syscall.EINVAL, nil},
	}

	var got strings.Builder
	err := drainKmsg(read.read, func(record string) error {
		got.WriteString(record)
		return nil
	})
	if err != nil {
		t.Fatalf("drainKmsg() error = %v, want nil", err)
	}

	want := "6,1,100,0;before\n6,2,200,0;after\n"
	if got.String() != want {
		t.Fatalf("drainKmsg() records = %q, want %q", got.String(), want)
	}
	if read.seenBufLen < 2048 {
		t.Fatalf("drainKmsg() buffer size = %d, want >= 2048 to fit legal records", read.seenBufLen)
	}
}

// TestDrainKmsgPropagatesOtherErrors verifies non-EAGAIN/EINVAL errors stay
// fatal.
func TestDrainKmsgPropagatesOtherErrors(t *testing.T) {
	read := &scriptedKmsgRead{
		chunks: [][]byte{[]byte("6,1,100,0;x\n")},
		errs:   []error{syscall.EIO},
	}

	err := drainKmsg(read.read, func(string) error { return nil })
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("drainKmsg() error = %v, want %v", err, syscall.EIO)
	}
}

// TestDrainKmsgStopsOnEAGAIN verifies the drain returns nil once the device is
// empty.
func TestDrainKmsgStopsOnEAGAIN(t *testing.T) {
	read := &scriptedKmsgRead{
		chunks: [][]byte{[]byte("6,1,100,0;only\n")},
		errs:   []error{nil},
	}

	var got strings.Builder
	if err := drainKmsg(read.read, func(r string) error {
		got.WriteString(r)
		return nil
	}); err != nil {
		t.Fatalf("drainKmsg() error = %v, want nil", err)
	}
	if got.String() != "6,1,100,0;only\n" {
		t.Fatalf("drainKmsg() records = %q", got.String())
	}
}
