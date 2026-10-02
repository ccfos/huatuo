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

package bpf

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRateLimiterConstants(t *testing.T) {
	t.Parallel()

	t.Run("disabled returns original map", func(t *testing.T) {
		t.Parallel()

		limiter := NewRateLimiter("tcp_retransmit", 0)
		consts := map[string]any{"existing": uint64(7)}
		got := limiter.Constants(consts)

		if len(got) != 1 || got["existing"] != uint64(7) {
			t.Fatalf("constants = %#v, want original map only", got)
		}
	})

	t.Run("enabled initializes nil map", func(t *testing.T) {
		t.Parallel()

		limiter := NewRateLimiter("tcp_retransmit", 100)
		got := limiter.Constants(nil)

		if got["bpf_rlimit_interval_ns_tcp_retransmit"] != uint64(time.Second) {
			t.Fatalf(
				"interval = %v, want %d",
				got["bpf_rlimit_interval_ns_tcp_retransmit"],
				time.Second,
			)
		}
		if got["bpf_rlimit_burst_tcp_retransmit"] != uint64(100) {
			t.Fatalf("burst = %v, want 100", got["bpf_rlimit_burst_tcp_retransmit"])
		}
		if got["bpf_rlimit_max_burst_tcp_retransmit"] != uint64(0) {
			t.Fatalf("max burst = %v, want 0", got["bpf_rlimit_max_burst_tcp_retransmit"])
		}
	})

	t.Run("enabled preserves existing constants", func(t *testing.T) {
		t.Parallel()

		limiter := NewRateLimiter("tcp_retransmit", 10)
		consts := map[string]any{"existing": uint64(7)}
		got := limiter.Constants(consts)

		if got["existing"] != uint64(7) {
			t.Fatalf("existing constant = %v, want 7", got["existing"])
		}
		if got["bpf_rlimit_burst_tcp_retransmit"] != uint64(10) {
			t.Fatalf("burst = %v, want 10", got["bpf_rlimit_burst_tcp_retransmit"])
		}
	})
}

func TestRateLimiterEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		eventsPerSecond uint64
		want            bool
	}{
		{name: "disabled", eventsPerSecond: 0, want: false},
		{name: "enabled", eventsPerSecond: 1, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := NewRateLimiter("tcp_retransmit", tt.eventsPerSecond).Enabled()
			if got != tt.want {
				t.Fatalf("Enabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestRateLimiterReadEventsReturnsUnexpectedReadError(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read failed")
	reader := &rateLimitReaderStub{readErr: readErr}
	limiter := NewRateLimiter("tcp_retransmit", 100)
	limiter.reader = reader
	err := limiter.ReadEvents(context.Background())
	if !errors.Is(err, readErr) {
		t.Fatalf("ReadEvents() error = %v, want %v", err, readErr)
	}
}

func TestRateLimiterReadEventsRequiresOpenEventPipe(t *testing.T) {
	t.Parallel()

	err := NewRateLimiter("tcp_retransmit", 100).ReadEvents(context.Background())
	if !errors.Is(err, errRateLimitEventPipeNotOpen) {
		t.Fatalf(
			"ReadEvents() error = %v, want %v",
			err,
			errRateLimitEventPipeNotOpen,
		)
	}
}

func TestRateLimiterReadEventsStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &rateLimitReaderStub{readErr: errors.New("must not read")}
	limiter := NewRateLimiter("tcp_retransmit", 100)
	limiter.reader = reader
	if err := limiter.ReadEvents(ctx); err != nil {
		t.Fatalf("ReadEvents() error = %v, want nil", err)
	}
	if reader.reads != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.reads)
	}
}

func TestRateLimiterOpenEventPipeRejectsDuplicateOpen(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter("tcp_retransmit", 100)
	limiter.reader = &rateLimitReaderStub{}
	err := limiter.OpenEventPipe(context.Background(), nil)
	if !errors.Is(err, errRateLimitEventPipeAlreadyOpen) {
		t.Fatalf(
			"OpenEventPipe() error = %v, want %v",
			err,
			errRateLimitEventPipeAlreadyOpen,
		)
	}
}

func TestRateLimiterCloseEventPipe(t *testing.T) {
	t.Parallel()

	t.Run("before open", func(t *testing.T) {
		t.Parallel()

		if err := NewRateLimiter("tcp_retransmit", 100).CloseEventPipe(); err != nil {
			t.Fatalf("CloseEventPipe() error = %v, want nil", err)
		}
	})

	t.Run("reader error", func(t *testing.T) {
		t.Parallel()

		closeErr := errors.New("close failed")
		reader := &rateLimitReaderStub{closeErr: closeErr}
		limiter := NewRateLimiter("tcp_retransmit", 100)
		limiter.reader = reader

		err := limiter.CloseEventPipe()
		if !errors.Is(err, closeErr) {
			t.Fatalf("CloseEventPipe() error = %v, want %v", err, closeErr)
		}
		if reader.closes != 1 {
			t.Fatalf("reader close calls = %d, want 1", reader.closes)
		}
	})
}

type rateLimitReaderStub struct {
	readErr  error
	closeErr error
	reads    int
	closes   int
}

func (r *rateLimitReaderStub) ReadInto(any) error {
	r.reads++
	return r.readErr
}

func (*rateLimitReaderStub) ReadBatch(func() any) (PerfEventBatch, error) {
	return PerfEventBatch{}, nil
}

func (r *rateLimitReaderStub) Close() error {
	r.closes++
	return r.closeErr
}
