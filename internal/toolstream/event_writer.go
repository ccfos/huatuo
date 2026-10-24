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
	"net"
	"sync"
	"time"
)

// EventCloseTimeout bounds best-effort event-stream finalization.
const EventCloseTimeout = 5 * time.Second

// ErrEventOutputCanceled marks an event send interrupted by its exhausted
// cancellation grace. Independent encoding and transport failures do not match it.
var ErrEventOutputCanceled = errors.New("toolstream: event output canceled")

// EventWriter owns an event connection until Close. Live runs have a bounded
// cancellation grace; finite final reports have a hard deadline. Both bound End
// delivery and interrupt stalled sends. Serialize Write and Close calls.
type EventWriter struct {
	sendFrame        func(context.Context, any, SendOptions) error
	closeClient      func() error
	runCtx           context.Context
	sendCtx          context.Context
	stopCancellation func() bool
	connectionOnce   sync.Once
	connectionErr    error
	finishOnce       sync.Once
	finishErr        error
	finalMu          sync.Mutex
	finalCtx         context.Context
	cancelFinal      context.CancelFunc
}

// NewEventWriter takes ownership of a connected client for one run. Binding
// cancellation once avoids adding a cancellation callback for every sample.
func NewEventWriter(ctx context.Context, client *Client) *EventWriter {
	writer := newEventWriter(ctx, client)
	writer.stopCancellation = context.AfterFunc(ctx, func() {
		writer.finalizationContext()
	})

	return writer
}

// NewFinalEventWriter takes ownership of a client for a finite final report.
// The report and End share one hard timeout, without a second cancellation grace.
func NewFinalEventWriter(ctx context.Context, client *Client) *EventWriter {
	writer := newEventWriter(ctx, client)
	writer.finalCtx, writer.cancelFinal = context.WithTimeout(ctx, EventCloseTimeout)
	writer.sendCtx = writer.finalCtx
	writer.stopCancellation = context.AfterFunc(writer.finalCtx, func() {
		_ = writer.closeConnection()
	})

	return writer
}

func newEventWriter(ctx context.Context, client *Client) *EventWriter {
	return &EventWriter{
		sendFrame:   client.Send,
		closeClient: client.Close,
		runCtx:      ctx,
		sendCtx:     context.WithoutCancel(ctx),
	}
}

// Write sends an event within the writer's lifecycle and finalization budget.
// Only exhausted run cancellation matches ErrEventOutputCanceled; independent
// encoding and transport errors remain failures even when the run is canceled.
func (w *EventWriter) Write(event any) error {
	ctx := w.sendCtx
	if w.runCtx.Err() != nil {
		ctx = w.finalizationContext()
	}

	err := w.sendFrame(ctx, event, SendOptions{})
	if err == nil {
		return nil
	}

	if w.runCtx.Err() != nil && eventSendInterrupted(w.finalizationContext(), err) {
		return errors.Join(ErrEventOutputCanceled, w.runCtx.Err())
	}

	return err
}

func eventSendInterrupted(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		return false
	}

	var marshalErr *json.MarshalerError
	if errors.As(err, &marshalErr) {
		return false
	}

	// Matching a wrapped/joined context error would hide an independent failure.
	isContextError := err == ctx.Err() //nolint:errorlint
	return isContextError || errors.Is(err, net.ErrClosed)
}

// The cancellation callback also closes an already-blocked live send when the
// grace expires. All final frames share this deadline; none renews the budget.
func (w *EventWriter) finalizationContext() context.Context {
	w.finalMu.Lock()
	defer w.finalMu.Unlock()

	if w.finalCtx == nil {
		deadline := time.Now().Add(EventCloseTimeout)
		if runDeadline, ok := w.runCtx.Deadline(); w.runCtx.Err() == nil && ok && runDeadline.Before(deadline) {
			deadline = runDeadline
		}

		w.finalCtx, w.cancelFinal = context.WithDeadline(w.sendCtx, deadline)
		context.AfterFunc(w.finalCtx, func() { _ = w.closeConnection() })
	}

	return w.finalCtx
}

// Close sends End within the remaining finalization budget and always releases
// the connection. Repeated calls return the original close result.
func (w *EventWriter) Close() error {
	w.finishOnce.Do(func() {
		canceledAtClose := w.runCtx.Err() != nil
		w.stopCancellation()
		ctx := w.finalizationContext()
		defer w.cancelFinal()

		if canceledAtClose && ctx.Err() != nil {
			w.finishErr = w.closeConnection()
			return
		}

		sendErr := w.sendFrame(ctx, nil, SendOptions{End: true})
		// A canceled run may exhaust its best-effort grace against a stalled
		// receiver. Preserve real transport errors and normal-close timeouts.
		if canceledAtClose && eventSendInterrupted(ctx, sendErr) {
			sendErr = nil
		}

		w.finishErr = errors.Join(sendErr, w.closeConnection())
	})

	return w.finishErr
}

func (w *EventWriter) closeConnection() error {
	w.connectionOnce.Do(func() {
		w.connectionErr = w.closeClient()
		// Transport cancellation can release the same connection first.
		// Releasing an already-closed connection still fulfills ownership.
		if errors.Is(w.connectionErr, net.ErrClosed) {
			w.connectionErr = nil
		}
	})

	return w.connectionErr
}
