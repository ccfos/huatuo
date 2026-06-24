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
	"encoding/json"
	"fmt"

	"huatuo-bamai/internal/toolstream/transport"
)

// ClientOptions configures a Client connection.
type ClientOptions struct {
	SockPath string
	ToolName string
	Version  string
	TaskID   string
}

// Client is the tool-side peer; use NewClient to create one.
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

// Send encodes event and sends it as a payload frame.
func (c *Client) Send(event any) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	return c.inner.SendChunk(payload, false)
}

// End sends the end-of-stream marker and closes the connection; safe for defer.
func (c *Client) End() {
	c.inner.End()
}

// Close closes the connection without sending an end frame; prefer End.
func (c *Client) Close() error {
	return c.inner.Close()
}
