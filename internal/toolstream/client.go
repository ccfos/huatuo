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
	"fmt"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

// ClientOptions configures a Client connection.
type ClientOptions struct {
	SockPath string
	ToolName string
	Version  string
	TaskID   string
}

// Client is the tool-side peer; use NewClient to create one.
// Serialize Send calls; Close may interrupt an in-flight send.
type Client struct {
	inner *transport.Client
}

// NewClient connects to the server and returns a ready Client.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.SockPath == "" {
		return nil, fmt.Errorf("SockPath must not be empty")
	}
	if opts.ToolName == "" {
		return nil, fmt.Errorf("ToolName must not be empty")
	}
	if opts.Version == "" {
		return nil, fmt.Errorf("Version must not be empty")
	}

	conn, err := transport.NewClient(opts.SockPath, opts.ToolName, opts.Version, opts.TaskID)
	if err != nil {
		return nil, err
	}

	return &Client{inner: conn}, nil
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

// Close releases the connection without sending a protocol frame.
func (c *Client) Close() error { return c.inner.Close() }
