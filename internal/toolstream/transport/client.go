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
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

// Client is the transport-side connection; use NewClient to create one.
type Client struct {
	encoder *capnp.Encoder
	conn    net.Conn

	closeOnce sync.Once
	closeErr  error
}

// handshake writes the one-shot ConnectRequest frame. UDS has no handshake;
// the server reads this frame, parses metadata, and starts dispatching chunks.
// There is no server ack.
func (c *Client) handshake(serviceName, toolName, version, taskID string) error {
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

	if err := connect.SetServiceName(serviceName); err != nil {
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

	connect.SetProtoVersion(3)

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

// SendOptions controls stream termination and acknowledgement.
type SendOptions struct {
	End    bool
	Commit bool
}

// Send writes one frame. Commit requires End and waits for its acknowledgement.
// Callers serialize sends; Close may interrupt a blocked write or acknowledgement.
func (c *Client) Send(data []byte, opts SendOptions) error {
	if opts.Commit && !opts.End {
		return errors.New("toolstream: commit requires end")
	}

	if err := c.writeFrame(func(chunk Chunk) error {
		chunk.SetEnd(opts.End)
		chunk.SetCommit(opts.Commit)
		if len(data) != 0 {
			return chunk.SetData(data)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("send frame: %w", err)
	}
	if !opts.Commit {
		return nil
	}
	return c.receiveCommitResult()
}

// SetDeadline applies to both pending and subsequent protocol reads and writes.
func (c *Client) SetDeadline(deadline time.Time) error {
	return c.conn.SetDeadline(deadline)
}

// Close releases the connection without sending a protocol frame.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.conn.Close()
	})

	return c.closeErr
}

func (c *Client) receiveCommitResult() error {
	msg, err := capnp.NewDecoder(c.conn).Decode()
	if err != nil {
		return fmt.Errorf("receive commit result (outcome unconfirmed): %w", err)
	}
	defer msg.Release()
	result, err := ReadRootMessage(msg)
	if err != nil {
		return err
	}
	if result.Which() != Message_Which_result {
		return errors.New("receive commit result: unexpected frame")
	}
	ack, err := result.Result()
	if err != nil {
		return err
	}
	failure, err := ack.Error()
	if err != nil {
		return err
	}
	if failure != "" {
		return fmt.Errorf("remote commit failed: %s", failure)
	}
	return nil
}

// NewClient dials path, sends the handshake, and returns a ready Client.
func NewClient(path, serviceName, toolName, version, taskID string) (*Client, error) {
	conn, err := DialUDS(path)
	if err != nil {
		return nil, err
	}

	c := &Client{
		encoder: capnp.NewEncoder(conn),
		conn:    conn,
	}
	if err := c.handshake(serviceName, toolName, version, taskID); err != nil {
		// The caller never sees the connection, so it must not leak.
		_ = c.Close()
		return nil, fmt.Errorf("transport: send connect: %w", err)
	}

	return c, nil
}
