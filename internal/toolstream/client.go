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
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

// EventCloseTimeout bounds best-effort event-stream finalization.
const EventCloseTimeout = 5 * time.Second

// ErrEventOutputCanceled marks an event send interrupted by its exhausted
// cancellation grace. Independent encoding and transport failures do not match it.
var ErrEventOutputCanceled = errors.New("toolstream: event output canceled")

// ClientOptions configures a Client connection.
type ClientOptions struct {
	SockPath    string
	ServiceName string // Required handler route.
	ToolName    string // Optional business source; never used for routing.
	Version     string
	TaskID      string

	// EventContext enables bounded event output for one live run.
	EventContext context.Context
}

// Client is the tool-side peer; use NewClient to create one.
// Serialize sends, FinishEvents and event setup; Close may interrupt a send.
type Client struct {
	inner *transport.Client

	// Event output is opt-in; Send retains its per-call context semantics.
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

// NewClient connects to the server and returns a ready Client.
func NewClient(opts *ClientOptions) (*Client, error) {
	if opts.SockPath == "" {
		return nil, fmt.Errorf("SockPath must not be empty")
	}

	if opts.ServiceName == "" {
		return nil, fmt.Errorf("ServiceName must not be empty")
	}

	if opts.Version == "" {
		return nil, fmt.Errorf("Version must not be empty")
	}

	conn, err := transport.NewClient(opts.SockPath, opts.ServiceName, opts.ToolName, opts.Version, opts.TaskID)
	if err != nil {
		return nil, err
	}

	client := &Client{inner: conn}
	if opts.EventContext != nil {
		client.initEvents(opts.EventContext)
		client.stopCancellation = context.AfterFunc(opts.EventContext, func() {
			client.finalizationContext()
		})
	}

	return client, nil
}

// SendOptions controls the protocol flags on a payload frame.
type SendOptions = transport.SendOptions

// Send encodes an optional event and sends it with the requested protocol flags.
// Commit waits for the server acknowledgement; Close releases the connection.
func (c *Client) Send(ctx context.Context, event any, opts SendOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var payload []byte
	if event != nil {
		var err error
		payload, err = json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
	}

	return c.inner.Send(ctx, payload, opts)
}

// Close releases the connection without sending End. Event clients also stop
// their cancellation callbacks and timers; repeated event closes are idempotent.
func (c *Client) Close() error {
	if c.runCtx == nil {
		return c.inner.Close()
	}

	c.stopCancellation()
	// Establish the shared context before canceling it so an already-running
	// cancellation callback cannot create a new timer after the close.
	c.finalizationContext()
	c.cancelFinal()

	return c.closeConnection()
}

// BeginFinalReport starts one hard deadline shared by a finite report and End.
// Call after collection and before sending; it cannot replace a live event run.
func (c *Client) BeginFinalReport(ctx context.Context) error {
	if c.runCtx != nil {
		return errors.New("toolstream: event output is already configured")
	}

	c.initEvents(ctx)
	c.finalCtx, c.cancelFinal = context.WithTimeout(ctx, EventCloseTimeout)
	c.sendCtx = c.finalCtx
	c.stopCancellation = context.AfterFunc(c.finalCtx, func() {
		_ = c.closeConnection()
	})

	return nil
}

func (c *Client) initEvents(ctx context.Context) {
	c.runCtx = ctx
	c.sendCtx = context.WithoutCancel(ctx)
	c.sendFrame = c.Send
	c.closeClient = c.inner.Close
}

// SendEvent sends an event within the client's event lifecycle and finalization budget.
// Only exhausted run cancellation matches ErrEventOutputCanceled; independent
// encoding and transport errors remain failures even when the run is canceled.
func (c *Client) SendEvent(event any) error {
	if c.runCtx == nil {
		return errors.New("toolstream: event output is not configured")
	}

	ctx := c.sendCtx
	if c.runCtx.Err() != nil {
		ctx = c.finalizationContext()
	}

	err := c.sendFrame(ctx, event, SendOptions{})
	if err == nil {
		return nil
	}

	if c.runCtx.Err() != nil && eventSendInterrupted(c.finalizationContext(), err) {
		return errors.Join(ErrEventOutputCanceled, c.runCtx.Err())
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
func (c *Client) finalizationContext() context.Context {
	c.finalMu.Lock()
	defer c.finalMu.Unlock()

	if c.finalCtx == nil {
		deadline := time.Now().Add(EventCloseTimeout)
		if runDeadline, ok := c.runCtx.Deadline(); c.runCtx.Err() == nil && ok && runDeadline.Before(deadline) {
			deadline = runDeadline
		}

		c.finalCtx, c.cancelFinal = context.WithDeadline(c.sendCtx, deadline)
		context.AfterFunc(c.finalCtx, func() { _ = c.closeConnection() })
	}

	return c.finalCtx
}

// FinishEvents sends End within the remaining budget and releases the connection.
// Repeated calls return the original finalization result.
func (c *Client) FinishEvents() error {
	if c.runCtx == nil {
		return errors.New("toolstream: event output is not configured")
	}

	c.finishOnce.Do(func() {
		canceledAtClose := c.runCtx.Err() != nil
		c.stopCancellation()
		ctx := c.finalizationContext()
		defer c.cancelFinal()

		if canceledAtClose && ctx.Err() != nil {
			c.finishErr = c.closeConnection()
			return
		}

		sendErr := c.sendFrame(ctx, nil, SendOptions{End: true})
		// A canceled run may exhaust its best-effort grace against a stalled
		// receiver. Preserve real transport errors and normal-close timeouts.
		if canceledAtClose && eventSendInterrupted(ctx, sendErr) {
			sendErr = nil
		}

		c.finishErr = errors.Join(sendErr, c.closeConnection())
	})

	return c.finishErr
}

func (c *Client) closeConnection() error {
	c.connectionOnce.Do(func() {
		c.connectionErr = c.closeClient()
		// Transport cancellation can release the same connection first.
		// Releasing an already-closed connection still fulfills ownership.
		if errors.Is(c.connectionErr, net.ErrClosed) {
			c.connectionErr = nil
		}
	})

	return c.connectionErr
}
