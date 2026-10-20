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
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"

	capnp "capnproto.org/go/capnp/v3"
)

// Client is the transport-side connection; use NewClient to create one.
type Client struct {
	encoder *capnp.Encoder
	conn    net.Conn
	stop    func() bool
}

// handshake writes the one-shot ConnectRequest frame. UDS has no handshake;
// the server reads this frame, parses metadata, and starts dispatching chunks.
// There is no server ack.
func (c *Client) handshake(serviceName, toolName, version, taskID string, protoVersion uint32) error {
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

	connect.SetProtoVersion(protoVersion)

	return c.encoder.Encode(m)
}

func encodeFrame(setup func(Chunk) error) ([]byte, error) {
	m, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		return nil, err
	}
	defer m.Release()

	root, err := NewRootMessage(seg)
	if err != nil {
		return nil, err
	}

	chunk, err := root.NewChunk()
	if err != nil {
		return nil, err
	}

	if err := setup(chunk); err != nil {
		return nil, err
	}

	return m.Marshal()
}

// SendOptions controls stream termination and acknowledgement.
type SendOptions struct {
	End    bool
	Commit bool
}

// Send writes one frame. Commit requires End and waits for its acknowledgement.
// Cancellation closes the connection; callers serialize sends on each client.
func (c *Client) Send(ctx context.Context, data []byte, opts SendOptions) error {
	if opts.Commit && !opts.End {
		return errors.New("toolstream: commit requires end")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Event writers may use a non-cancelable context on every sample.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
		defer stop()
	}
	frame, err := encodeFrame(func(chunk Chunk) error {
		chunk.SetEnd(opts.End)
		chunk.SetCommit(opts.Commit)
		if len(data) != 0 {
			return chunk.SetData(data)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.SendFrame(frame); err != nil {
		return fmt.Errorf("send frame: %w", errors.Join(err, ctx.Err()))
	}
	if !opts.Commit {
		return nil
	}
	return c.receiveCommitResult(ctx)
}

// SendFrame writes one chunk frame previously returned by EncodeChunkFrame.
// Queueing senders encode once and reuse the frame instead of re-encoding the
// payload for every write attempt.
func (c *Client) SendFrame(frame []byte) error {
	for len(frame) > 0 {
		n, err := c.conn.Write(frame)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// Close releases the connection without sending a protocol frame.
func (c *Client) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return c.conn.Close()
}

func (c *Client) receiveCommitResult(ctx context.Context) error {
	msg, err := capnp.NewDecoder(c.conn).Decode()
	if err != nil {
		return fmt.Errorf("receive commit result (outcome unconfirmed): %w", errors.Join(err, ctx.Err()))
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

// ClientMetadata identifies the tool and task in the connection handshake.
// Clients always send the protocol version implemented by this package.
type ClientMetadata struct {
	ServiceName string
	ToolName    string
	Version     string
	TaskID      string
}

// NewClient dials path, sends the handshake, and returns a ready Client.
func NewClient(path, serviceName, toolName, version, taskID string) (*Client, error) {
	conn, err := DialUDS(path)
	if err != nil {
		return nil, err
	}

	return NewClientWithConn(conn, ClientMetadata{ServiceName: serviceName, ToolName: toolName, Version: version, TaskID: taskID})
}

// NewClientWithConn takes ownership of conn, including handshake failures.
func NewClientWithConn(conn net.Conn, metadata ClientMetadata) (*Client, error) {
	return NewClientWithConnContext(context.Background(), conn, metadata)
}

// NewClientWithConnContext closes conn when ctx is canceled, interrupting
// both handshake writes and later sends. The caller owns ctx's lifetime.
func NewClientWithConnContext(ctx context.Context, conn net.Conn, metadata ClientMetadata) (*Client, error) {
	if conn == nil {
		return nil, errors.New("transport: connection must not be nil")
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	c := &Client{encoder: capnp.NewEncoder(conn), conn: conn}
	c.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := c.handshake(metadata.ServiceName, metadata.ToolName, metadata.Version, metadata.TaskID, ProtocolVersion); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("transport: send connect: %w", errors.Join(err, ctx.Err()))
	}
	return c, nil
}

// ChunkFrameSize returns the encoded size without allocating a payload buffer.
// A single-segment frame has an 8-byte header, an 8-byte root pointer,
// a 16-byte Message and a 32-byte Chunk. Data is padded to an 8-byte word.
func ChunkFrameSize(dataSize int) (int, error) {
	const overhead = 64
	const wordSize = 8
	if dataSize < 0 || dataSize > math.MaxInt-overhead-(wordSize-1) {
		return 0, fmt.Errorf("invalid chunk payload size %d", dataSize)
	}
	return overhead + (dataSize+wordSize-1)/wordSize*wordSize, nil
}

// EncodeChunkFrame returns one complete Chunk frame with the given payload.
// The returned slice is owned by the caller.
func EncodeChunkFrame(data []byte) ([]byte, error) {

	return encodeFrame(func(chunk Chunk) error {
		if len(data) > 0 {
			if err := chunk.SetData(data); err != nil {
				return err
			}
		}
		return nil
	})
}
