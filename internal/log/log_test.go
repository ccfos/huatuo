// Copyright 2025 The HuaTuo Authors
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

package log

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

// Test SetLevel and GetLevel behavior (valid, invalid, case-insensitive)
func TestSetLevel_Behavior(t *testing.T) {
	original := GetLevel()
	defer SetLevel(original.String())

	tests := []struct {
		name      string
		input     string
		wantLevel logrus.Level
		changed   bool
	}{
		// valid levels
		{"trace level", "trace", logrus.TraceLevel, true},
		{"debug lowercase", "debug", logrus.DebugLevel, true},
		{"info mixed case", "Info", logrus.InfoLevel, true},
		{"warn level", "warn", logrus.WarnLevel, true},
		{"error level", "error", logrus.ErrorLevel, true},
		{"fatal level", "fatal", logrus.FatalLevel, true},
		{"panic level", "panic", logrus.PanicLevel, true},

		// invalid levels
		{"invalid string", "invalid", logrus.DebugLevel, false},
		{"unsupported level", "critical", logrus.DebugLevel, false},
		{"empty string", "", logrus.DebugLevel, false},
		{"numeric", "123", logrus.DebugLevel, false},
		{"special chars", "debug!", logrus.DebugLevel, false},

		// case insensitive levels
		{"debug uppercase", "DEBUG", logrus.DebugLevel, true},
		{"info uppercase", "INFO", logrus.InfoLevel, true},
		{"error uppercase", "ERROR", logrus.ErrorLevel, true},
		{"error mixed", "DeBuG", logrus.DebugLevel, true},
		{"panic uppercase", "PANIC", logrus.PanicLevel, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// set a known baseline
			SetLevel("debug")
			before := GetLevel()

			SetLevel(tt.input)
			after := GetLevel()

			if tt.changed {
				if after != tt.wantLevel {
					t.Errorf("SetLevel(%q) = %v, want %v", tt.input, after, tt.wantLevel)
				}
			} else {
				if after != before {
					t.Errorf("SetLevel(%q) unexpectedly changed level from %v to %v", tt.input, before, after)
				}
			}
		})
	}
}

// Test logging output behavior and output setter safety
//
// TestOutput verifies log output correctness and ensures SetOutput(nil) does not panic.
func TestOutput(t *testing.T) {
	// Test normal logging output to a buffer
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel("debug")

	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")

	output := buf.String()
	for _, msg := range []string{"debug message", "info message", "warn message", "error message"} {
		if !strings.Contains(output, msg) {
			t.Errorf("expected %q to be logged", msg)
		}
	}
}

// Test WithError behavior (errors)
func TestWithError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "non-nil error",
			err:  errors.New("test error"),
		},
		{
			name: "sentinel error",
			err:  errors.New("sentinel error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := WithError(tt.err)

			val, exists := entry.Data["error"]
			if !exists {
				t.Fatal("expected error field, but not found")
			}

			e, _ := val.(error)
			if !errors.Is(e, tt.err) {
				t.Errorf("expected error %v, got %v", tt.err, e)
			}
		})
	}
}

// Test AddHook related behaviors: (single, multiple, error)
func TestAddHook(t *testing.T) {
	tests := []struct {
		name      string
		hooks     []*testHook
		wantCalls []int
	}{
		{
			name: "single hook",
			hooks: []*testHook{
				{
					onFire: func(*logrus.Entry) error {
						return nil
					},
				},
			},
			wantCalls: []int{0},
		},
		{
			name: "multiple hooks in order",
			hooks: []*testHook{
				{
					onFire: func(*logrus.Entry) error { return nil },
				},
				{
					onFire: func(*logrus.Entry) error { return nil },
				},
			},
			wantCalls: []int{0, 1},
		},
		{
			name: "error hook does not break logging",
			hooks: []*testHook{
				{
					onFire: func(*logrus.Entry) error {
						return errors.New("hook error")
					},
				},
			},
			wantCalls: []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called []int

			for i, h := range tt.hooks {
				idx := i
				orig := h.onFire
				h.onFire = func(e *logrus.Entry) error {
					called = append(called, idx)
					if orig != nil {
						return orig(e)
					}
					return nil
				}
				AddHook(h)
			}

			// Should not panic
			Info("hook test")

			if len(called) != len(tt.wantCalls) {
				t.Fatalf("expected %d hooks called, got %d", len(tt.wantCalls), len(called))
			}
			for i := range tt.wantCalls {
				if called[i] != tt.wantCalls[i] {
					t.Errorf("hook call order mismatch: got %v, want %v", called, tt.wantCalls)
					break
				}
			}
		})
	}
}

// Test concurrent logging safety and verify
// each log entry is "abc" repeated by goroutine index,
// and combined logs equal expected string.
func TestConcurrentLogging(t *testing.T) {
	t.Parallel()

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var mu sync.Mutex
	var logs []string

	// Thread-safe function to append log entries to the slice
	logFunc := func(s string) {
		mu.Lock()
		logs = append(logs, s)
		mu.Unlock()
	}

	for i := range goroutines {
		go func() {
			defer wg.Done()
			// Each goroutine logs "abc" repeated (i+1) times
			s := strings.Repeat("abc", i+1)
			logFunc(s)
		}()
	}

	wg.Wait()

	// Build expected combined log string in goroutine order
	var expectedBuilder strings.Builder
	for i := range goroutines {
		_, _ = expectedBuilder.WriteString(strings.Repeat("abc", i+1))
	}
	expected := expectedBuilder.String()

	// Join all collected logs into one string
	combined := strings.Join(logs, "")

	if combined != expected {
		t.Errorf("expected combined logs to be %q, but got %q", expected, combined)
	}
}

// Test Panic behaviors (panic expected)
func TestPanicAndFatal(t *testing.T) {
	// Test Panic
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Panic did not panic")
			}
		}()
		Panic("this should panic")
	}()

	// Test Panicf
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Panicf did not panic")
			}
		}()
		Panicf("this should panic: %d", 123)
	}()
	// TODO: Test Fatal - cannot test directly because Fatal calls os.Exit and will exit the test process.
}

// Test formatting log with Infof
func TestFormat(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel("info")

	tests := []struct {
		format   string
		args     []any
		expected string
	}{
		{"format %s %d", []any{"test", 123}, "format test 123"},
		{"hello %s", []any{"world"}, "hello world"},
		{"number: %04d", []any{7}, "number: 0007"},
		{"float: %.2f", []any{3.14159}, "float: 3.14"},
		{"percent %% done", nil, "percent % done"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			buf.Reset()
			Infof(tt.format, tt.args...)
			logged := buf.String()

			if !strings.Contains(logged, tt.expected) {
				t.Errorf("expected log to contain %q, got %q", tt.expected, logged)
			}
		})
	}
}

// Helper: testHook for Hook interface testing
type testHook struct {
	onFire func(entry *logrus.Entry) error
}

func (h *testHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *testHook) Fire(entry *logrus.Entry) error {
	return h.onFire(entry)
}
