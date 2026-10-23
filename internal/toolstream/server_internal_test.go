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

package toolstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	testutils "github.com/ccfos/huatuo/internal/testing"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestClientEventSetupRequired(t *testing.T) {
	client := &Client{}
	if err := client.SendEvent("event"); err == nil {
		t.Fatal("SendEvent accepted an unconfigured event lifecycle")
	}

	if err := client.FinishEvents(); err == nil {
		t.Fatal("FinishEvents accepted an unconfigured event lifecycle")
	}

	client.runCtx = t.Context()
	if err := client.BeginFinalReport(t.Context()); err == nil {
		t.Fatal("BeginFinalReport replaced an existing event lifecycle")
	}
}

func TestClientEventCloseStopsFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := false
	closeCalls := 0
	client := &Client{
		runCtx:  ctx,
		sendCtx: context.WithoutCancel(ctx),
		stopCancellation: func() bool {
			stopped = true
			return true
		},
		closeClient: func() error {
			closeCalls++
			return nil
		},
	}
	for range 2 {
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}

	cancel()
	// An already-running cancellation callback must reuse the canceled budget.
	if err := client.finalizationContext().Err(); err != context.Canceled {
		t.Fatalf("finalization context after Close = %v, want canceled", err)
	}

	if !stopped || closeCalls != 1 {
		t.Fatalf("Close: callback stopped=%t, connection releases=%d", stopped, closeCalls)
	}
}

func TestClientEventCancellationClassifiesOnlyInterruptedSends(t *testing.T) {
	failure := errors.New("encode or write failed")
	for _, test := range []struct {
		name          string
		sendErr       error
		isInterrupted bool
	}{
		{
			name:          "bare context interruption",
			sendErr:       context.DeadlineExceeded,
			isInterrupted: true,
		},
		{
			name:          "locally closed socket",
			sendErr:       fmt.Errorf("send frame: %w", net.ErrClosed),
			isInterrupted: true,
		},
		{
			name:    "independent failure joined with deadline",
			sendErr: errors.Join(failure, context.DeadlineExceeded),
		},
		{
			name: "marshal error wrapping a closed resource",
			sendErr: &json.MarshalerError{
				Type: reflect.TypeOf("event"),
				Err:  net.ErrClosed,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runCtx, cancel := context.WithCancel(t.Context())
			cancel()

			finalCtx, cancelFinal := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancelFinal()
			writer := &Client{
				runCtx:   runCtx,
				finalCtx: finalCtx,
				sendFrame: func(context.Context, any, SendOptions) error {
					return test.sendErr
				},
			}
			err := writer.SendEvent("event")
			if got := errors.Is(err, ErrEventOutputCanceled); got != test.isInterrupted {
				t.Fatalf(
					"Write = %v, output cancellation = %v, want %v",
					err,
					got,
					test.isInterrupted,
				)
			}

			if !test.isInterrupted && !errors.Is(err, test.sendErr) {
				t.Fatalf("Write = %v, want original failure %v", err, test.sendErr)
			}
		})
	}
}

func TestClientEventCloseAfterCanceledRelease(t *testing.T) {
	closeErr := errors.New("release connection failed")
	for _, test := range []struct {
		name     string
		closeErr error
		wantErr  error
	}{
		{
			name:     "transport already closed the connection",
			closeErr: net.ErrClosed,
		},
		{
			name:     "wrapped already closed connection",
			closeErr: fmt.Errorf("close unix connection: %w", net.ErrClosed),
		},
		{
			name:     "real release failure remains visible",
			closeErr: closeErr,
			wantErr:  closeErr,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			closeCalls := 0
			writer := &Client{
				runCtx:   ctx,
				sendCtx:  context.WithoutCancel(ctx),
				finalCtx: ctx,
				closeClient: func() error {
					closeCalls++
					return test.closeErr
				},
				stopCancellation: func() bool { return true },
				cancelFinal:      func() {},
			}
			for range 2 {
				if err := writer.FinishEvents(); !errors.Is(err, test.wantErr) {
					t.Fatalf("Close = %v, want %v", err, test.wantErr)
				}
			}

			if closeCalls != 1 {
				t.Fatalf("connection release calls = %d, want 1", closeCalls)
			}
		})
	}
}

func TestClientEventCanceledEndTimeoutIgnoresDuplicateClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	finalCtx, cancelFinal := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancelFinal()
	sentEnd := false
	writer := &Client{
		runCtx:      ctx,
		sendCtx:     context.WithoutCancel(ctx),
		finalCtx:    finalCtx,
		cancelFinal: cancelFinal,
		sendFrame: func(sendCtx context.Context, event any, options SendOptions) error {
			if event != nil || !options.End {
				t.Fatal("Close did not send an End-only frame")
			}

			sentEnd = true
			<-sendCtx.Done()
			return sendCtx.Err()
		},
		// The transport deadline may release the connection before its owner.
		closeClient:      func() error { return net.ErrClosed },
		stopCancellation: func() bool { return true },
	}
	for range 2 {
		if err := writer.FinishEvents(); err != nil {
			t.Fatalf("Close after canceled End timeout: %v", err)
		}
	}

	if !sentEnd {
		t.Fatal("End timeout was not exercised")
	}
}

func TestClientEventCloseKeepsRunDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	closeErr := errors.New("close connection failed")
	closeCalls := 0
	writer := &Client{
		runCtx:  ctx,
		sendCtx: context.WithoutCancel(ctx),
		sendFrame: func(sendCtx context.Context, event any, options SendOptions) error {
			if event != nil || !options.End {
				t.Fatal("Close did not send an End-only frame")
			}

			wantDeadline, _ := ctx.Deadline()
			gotDeadline, ok := sendCtx.Deadline()
			if !ok || !gotDeadline.Equal(wantDeadline) {
				t.Fatalf("End deadline = %v, want run deadline %v", gotDeadline, wantDeadline)
			}

			<-sendCtx.Done()
			return sendCtx.Err()
		},
		closeClient: func() error {
			closeCalls++
			return closeErr
		},
		stopCancellation: func() bool { return true },
	}
	if err := writer.FinishEvents(); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, closeErr) {
		t.Fatalf("Close = %v, want deadline and connection close errors", err)
	}

	if closeCalls != 1 {
		t.Fatalf("connection close calls = %d, want 1", closeCalls)
	}
}

func TestRequestsContinueAfterFrameFailures(t *testing.T) {
	server, err := NewServer(testutils.SocketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("first handler failed")
	var requests []*Request[int]
	Register(server, "test", func(_ context.Context, request *Request[int]) error {
		requests = append(requests, request)
		if len(requests) == 1 {
			return failure
		}
		return nil
	})
	session := &transport.Session{Context: t.Context(), ServiceName: "test", ToolName: "source", TaskID: "task", Version: "v1"}
	server.dispatch(session, transport.ChunkMsg{Data: []byte("1")})
	server.dispatch(session, transport.ChunkMsg{Data: []byte("bad json")})
	server.dispatch(session, transport.ChunkMsg{Data: []byte("2"), End: true, Commit: true})
	if len(requests) != 3 {
		t.Fatalf("handler received %d frames", len(requests))
	}
	if requests[0].PriorErr != nil || requests[0].Err != nil {
		t.Fatal("first frame inherited later errors")
	}
	if !errors.Is(requests[1].PriorErr, failure) || requests[1].Err == nil || requests[1].Data != nil {
		t.Fatalf("invalid decoding request: %+v", requests[1])
	}
	last := requests[2]
	if last.Data == nil || *last.Data != 2 || !last.Commit || !last.End || last.Err != nil || !errors.Is(last.PriorErr, failure) {
		t.Fatalf("final frame not delivered with prior error: %+v", last)
	}
	if last.ToolName != "source" || last.ServiceName != "test" || last.TaskID != "task" || last.Version != "v1" {
		t.Fatalf("metadata=%+v", last)
	}
	if !errors.Is(session.Err, failure) {
		t.Fatalf("session error lost: %v", session.Err)
	}
}

func TestRequestDecodeFailureDiscardsPartialData(t *testing.T) {
	for _, payload := range []string{`null`, `{"Value":1,"Count":"invalid"}`} {
		t.Run(payload, func(t *testing.T) {
			server, err := NewServer(testutils.SocketPath(t))
			if err != nil {
				t.Fatal(err)
			}

			type event struct {
				Value int
				Count int
			}
			called := false
			Register(server, "test", func(_ context.Context, request *Request[event]) error {
				called = true
				if request.Data != nil || request.Err == nil {
					t.Fatalf("decode failure request: %+v", request)
				}
				// A handler cannot erase the error acknowledged to the producer.
				request.Err = nil
				return nil
			})

			session := &transport.Session{Context: t.Context(), ServiceName: "test"}
			server.dispatch(session, transport.ChunkMsg{Data: []byte(payload), End: true, Commit: true})
			if !called || session.Err == nil {
				t.Fatalf("called=%v, session error=%v", called, session.Err)
			}
		})
	}
}
