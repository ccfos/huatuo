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

package transport

import (
	"fmt"
	"net"

	capnp "capnproto.org/go/capnp/v3"
)

// Client is the transport-side connection; use NewClient to create one.
type Client struct {
	encoder *capnp.Encoder
	conn    net.Conn
}

func (c *Client) handshake(toolName, version, taskID string) error {
	m, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		return err
	}
	defer m.Release()

	root, err := NewRootMessage(seg)
	if err != nil {
		return err
	}

	connect, err := root.NewConnect()
	if err != nil {
		return err
	}

	if err := connect.SetToolName(toolName); err != nil {
		return err
	}

	if err := connect.SetVersion(version); err != nil {
		return err
	}

	if err := connect.SetTaskID(taskID); err != nil {
		return err
	}

	connect.SetProtoVersion(1)

	return c.encoder.Encode(m)
}

func (c *Client) writeFrame(setup func(Chunk) error) error {
	m, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		return err
	}
	defer m.Release()

	root, err := NewRootMessage(seg)
	if err != nil {
		return err
	}

	chunk, err := root.NewChunk()
	if err != nil {
		return err
	}

	if err := setup(chunk); err != nil {
		return err
	}

	return c.encoder.Encode(m)
}

// SendChunk sends a single data frame.
// flush is reserved for future buffered-flush handlers.
func (c *Client) SendChunk(data []byte, flush bool) error {
	return c.writeFrame(func(chunk Chunk) error {
		if len(data) > 0 {
			if err := chunk.SetData(data); err != nil {
				return err
			}
		}

		chunk.SetFlush(flush)

		return nil
	})
}

// SendEnd signals a normal end of stream.
func (c *Client) SendEnd() error {
	return c.writeFrame(func(chunk Chunk) error {
		chunk.SetEnd(true)

		return nil
	})
}

// Close closes the connection; call SendEnd first for a clean shutdown.
func (c *Client) Close() error {
	return c.conn.Close()
}

// End calls SendEnd then Close; errors are discarded, safe for defer.
func (c *Client) End() {
	_ = c.SendEnd()
	_ = c.Close()
}

// NewClient dials path, sends the handshake, and returns a ready Client.
func NewClient(path, toolName, version, taskID string) (*Client, error) {
	conn, err := DialUDS(path)
	if err != nil {
		return nil, err
	}

	c := &Client{encoder: capnp.NewEncoder(conn), conn: conn}
	if err := c.handshake(toolName, version, taskID); err != nil {
		return nil, fmt.Errorf("transport: send connect: %w", err)
	}

	return c, nil
}
