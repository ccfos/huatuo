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
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

// DefaultSendTimeout is the default per-send timeout.
const DefaultSendTimeout = 5 * time.Second

var (
	// ErrOutputTimeout indicates output interrupted by an expired deadline.
	ErrOutputTimeout = errors.New("toolstream: output timed out")
	// ErrOutputCanceled indicates output interrupted by cancellation.
	ErrOutputCanceled = errors.New("toolstream: output canceled")
)

// ClientOptions configures a Client connection.
type ClientOptions struct {
	SockPath    string
	ServiceName string // Required handler route.
	ToolName    string // Optional business source; never used for routing.
	Version     string
	TaskID      string

	// SendTimeout limits each Send, including Commit acknowledgement.
	// Zero selects DefaultSendTimeout; negative values are invalid.
	SendTimeout time.Duration
}

// Client is the tool-side peer; use NewClient to create one.
// Serialize Send calls; Close may interrupt an in-flight send.
type Client struct {
	inner       *transport.Client
	sendTimeout time.Duration

	// terminalErr records the first failure that ends this protocol session.
	// Once set, Send performs no further I/O; the caller must Close.
	terminalErr error
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

	if opts.SendTimeout < 0 {
		return nil, errors.New("SendTimeout must not be negative")
	}

	sendTimeout := opts.SendTimeout
	if sendTimeout == 0 {
		sendTimeout = DefaultSendTimeout
	}

	conn, err := transport.NewClient(opts.SockPath, opts.ServiceName, opts.ToolName, opts.Version, opts.TaskID)
	if err != nil {
		return nil, err
	}

	return &Client{
		inner:       conn,
		sendTimeout: sendTimeout,
	}, nil
}

// SendOptions controls the protocol flags on a payload frame.
type SendOptions = transport.SendOptions

// Send encodes an optional event and sends it with the requested protocol flags.
// Commit waits for acknowledgement within the same deadline as the frame write.
// I/O failure prevents further sends; the caller must still Close the connection.
// Use errors.Is with ErrOutputTimeout or ErrOutputCanceled to distinguish causes.
func (c *Client) Send(ctx context.Context, event any, opts SendOptions) error {
	started := time.Now()
	if c.terminalErr != nil {
		return c.terminalErr
	}
	if err := outputContextError(ctx); err != nil {
		return err
	}
	// Reject this option before touching the connection so callers can correct it.
	if opts.Commit && !opts.End {
		return errors.New("toolstream: commit requires end")
	}

	var payload []byte
	if event != nil {
		var err error
		payload, err = json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
	}

	deadline, err := c.deadline(ctx, started)
	if err != nil {
		return err
	}
	if err := c.inner.SetDeadline(deadline); err != nil {
		c.terminalErr = fmt.Errorf("set send deadline: %w", err)
		return c.terminalErr
	}

	var cancelCause error
	if ctx.Done() == nil {
		err = c.inner.Send(payload, opts)
	} else {
		err, cancelCause = c.sendWithCancellation(ctx, deadline, payload, opts)
	}
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if cancelCause == nil {
				cancelCause = outputDeadlineError(ctx, deadline)
			}
			err = errors.Join(err, cancelCause)
		}
		c.terminalErr = err
		return c.terminalErr
	}

	return nil
}

// Keep cancellation separate so it cannot mask successful I/O or unrelated failures.
func (c *Client) sendWithCancellation(
	ctx context.Context,
	deadline time.Time,
	payload []byte,
	opts SendOptions,
) (sendErr, cancelCause error) {
	cancellationDone := make(chan struct{})
	var interruptErr error
	stopCancellation := context.AfterFunc(ctx, func() {
		defer close(cancellationDone)
		now := time.Now()
		if now.Before(deadline) {
			cancelCause = outputContextError(ctx)
		}
		interruptErr = c.inner.SetDeadline(now)
	})

	sendErr = c.inner.Send(payload, opts)
	if !stopCancellation() {
		// A late callback must not change the next send's deadline.
		<-cancellationDone
	}
	if interruptErr != nil {
		return errors.Join(sendErr, fmt.Errorf("interrupt send: %w", interruptErr)), nil
	}

	return sendErr, cancelCause
}

func (c *Client) deadline(ctx context.Context, started time.Time) (time.Time, error) {
	if err := outputContextError(ctx); err != nil {
		return time.Time{}, err
	}

	deadline := started.Add(c.sendTimeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}

	if !time.Now().Before(deadline) {
		return time.Time{}, outputDeadlineError(ctx, deadline)
	}

	return deadline, nil
}

func outputDeadlineError(ctx context.Context, deadline time.Time) error {
	var cause error
	if parent, ok := ctx.Deadline(); ok && parent.Equal(deadline) {
		// The socket timer may fire before the context timer publishes its cause.
		<-ctx.Done()
		cause = context.Cause(ctx)
	}

	return errors.Join(ErrOutputTimeout, context.DeadlineExceeded, cause)
}

func outputContextError(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}

	cause := context.Cause(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(ErrOutputTimeout, context.DeadlineExceeded, cause)
	}

	return errors.Join(ErrOutputCanceled, context.Canceled, cause)
}

// Close releases the connection without sending a protocol frame.
// Repeated calls return the original close result.
func (c *Client) Close() error { return c.inner.Close() }
