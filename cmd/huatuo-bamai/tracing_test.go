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

package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"huatuo-bamai/internal/toolstream"
)

type fakeToolstreamServer struct {
	drainErr   error
	closeErr   error
	drainErrs  []error
	closeErrs  []error
	drainCalls int
	closeCalls int
	calls      []string
	onDrain    func(context.Context, int)
}

func (s *fakeToolstreamServer) QuiesceAndDrain(ctx context.Context) error {
	s.calls = append(s.calls, "drain")
	call := s.drainCalls
	s.drainCalls++
	if s.onDrain != nil {
		s.onDrain(ctx, call)
	}
	if call < len(s.drainErrs) {
		return s.drainErrs[call]
	}
	return s.drainErr
}

func (s *fakeToolstreamServer) Close() error {
	s.calls = append(s.calls, "close")
	call := s.closeCalls
	s.closeCalls++
	if call < len(s.closeErrs) {
		return s.closeErrs[call]
	}
	return s.closeErr
}

func TestCloseToolstreamAlwaysCloses(t *testing.T) {
	drainErr := errors.New("drain failed")
	closeErr := errors.New("close failed")
	srv := &fakeToolstreamServer{drainErr: drainErr, closeErr: closeErr}

	err := closeToolstream(t.Context(), srv)
	if !errors.Is(err, drainErr) {
		t.Fatalf("closeToolstream error = %v, want drain error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("closeToolstream error = %v, want close error", err)
	}
	if want := []string{"drain", "close"}; !slices.Equal(srv.calls, want) {
		t.Fatalf("calls = %v, want %v", srv.calls, want)
	}
}

func TestCloseToolstreamReservesRetryBudget(t *testing.T) {
	firstDrainErr := errors.Join(toolstream.ErrDrainTimeout, context.DeadlineExceeded)
	var initialDeadline, retryDeadline time.Time
	srv := &fakeToolstreamServer{
		drainErrs: []error{firstDrainErr, nil},
		closeErrs: []error{toolstream.ErrHandlersActive, nil},
		onDrain: func(ctx context.Context, call int) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("drain call %d has no deadline", call)
			}
			if call == 0 {
				initialDeadline = deadline
				return
			}
			retryDeadline = deadline
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	if err := closeToolstream(ctx, srv); err != nil {
		t.Fatalf("closeToolstream() error = %v, want recovered shutdown", err)
	}
	if !initialDeadline.Before(retryDeadline) {
		t.Fatalf(
			"initial drain deadline = %v, retry deadline = %v; want reserved retry budget",
			initialDeadline,
			retryDeadline,
		)
	}
	if want := []string{"drain", "close", "drain", "close"}; !slices.Equal(srv.calls, want) {
		t.Fatalf("calls = %v, want %v", srv.calls, want)
	}
}
