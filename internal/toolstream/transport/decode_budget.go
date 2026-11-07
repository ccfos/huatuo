// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	capnp "capnproto.org/go/capnp/v3"
	"golang.org/x/sync/semaphore"
)

const (
	capnpWordSize           = 8
	maxStreamSegmentID      = 512
	defaultFrameReadTimeout = 10 * time.Second
)

// DecodeBudget bounds the total bytes held by frames being decoded or handled.
type DecodeBudget struct {
	maxBytes  uint64
	semaphore *semaphore.Weighted
}

// NewDecodeBudget creates a byte budget shared by one or more servers.
func NewDecodeBudget(maxBytes uint64) (*DecodeBudget, error) {
	if maxBytes == 0 || maxBytes > math.MaxInt64 {
		return nil, fmt.Errorf("transport: decode budget must be between 1 and %d bytes", uint64(math.MaxInt64))
	}
	return &DecodeBudget{
		maxBytes:  maxBytes,
		semaphore: semaphore.NewWeighted(int64(maxBytes)),
	}, nil
}

func (budget *DecodeBudget) acquire(ctx context.Context, bytes uint64) error {
	if bytes > budget.maxBytes {
		return fmt.Errorf("decode: message exceeds shared byte budget")
	}
	return budget.semaphore.Acquire(ctx, int64(bytes))
}

func (budget *DecodeBudget) release(bytes uint64) {
	budget.semaphore.Release(int64(bytes))
}

type messageDecoder struct {
	reader           io.Reader
	maxMessageSize   uint64
	budget           *DecodeBudget
	decoder          *capnp.Decoder
	frameReadTimeout time.Duration
}

func newMessageDecoder(
	reader io.Reader,
	maxMessageSize uint64,
	budget *DecodeBudget,
) *messageDecoder {
	decoder := capnp.NewDecoder(reader)
	decoder.MaxMessageSize = maxMessageSize
	timeout := time.Duration(0)
	if budget != nil {
		timeout = defaultFrameReadTimeout
	}
	return &messageDecoder{
		reader:           reader,
		maxMessageSize:   maxMessageSize,
		budget:           budget,
		decoder:          decoder,
		frameReadTimeout: timeout,
	}
}

type decodedMessage struct {
	message       *capnp.Message
	budget        *DecodeBudget
	reservedBytes uint64
	releaseOnce   sync.Once
}

func (message *decodedMessage) Release() {
	if message == nil {
		return
	}
	message.releaseOnce.Do(func() {
		message.message.Release()
		if message.budget != nil {
			message.budget.release(message.reservedBytes)
		}
	})
}

func (decoder *messageDecoder) Decode(ctx context.Context) (*decodedMessage, error) {
	if decoder.budget == nil && decoder.frameReadTimeout == 0 {
		message, err := decoder.decoder.Decode()
		if err != nil {
			return nil, err
		}
		return &decodedMessage{message: message}, nil
	}

	frame, reservedBytes, err := decoder.readBudgetedFrame(ctx)
	if err != nil {
		return nil, err
	}
	message, err := capnp.Unmarshal(frame)
	if err != nil {
		if decoder.budget != nil {
			decoder.budget.release(reservedBytes)
		}
		return nil, err
	}
	return &decodedMessage{
		message:       message,
		budget:        decoder.budget,
		reservedBytes: reservedBytes,
	}, nil
}

func (decoder *messageDecoder) readBudgetedFrame(ctx context.Context) ([]byte, uint64, error) {
	maxMessageSize := decoder.maxMessageSize
	if maxMessageSize < capnpWordSize {
		return nil, 0, errors.New("decode: max message size is smaller than header size")
	}

	var firstWord [capnpWordSize]byte
	headerStart := 0
	if conn, ok := decoder.reader.(net.Conn); ok {
		// Idle connections hold no budget. Start the timeout only when a frame
		// arrives, and bound both budget acquisition and the remaining reads.
		if _, err := io.ReadFull(conn, firstWord[:1]); err != nil {
			return nil, 0, err
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, decoder.frameReadTimeout)
		defer cancel()
		deadline, _ := ctx.Deadline()
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, 0, err
		}
		defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
		headerStart = 1
	}
	if _, err := io.ReadFull(decoder.reader, firstWord[headerStart:]); err == io.EOF && headerStart == 0 {
		return nil, 0, io.EOF
	} else if err != nil {
		return nil, 0, fmt.Errorf("decode: read header: %w", err)
	}

	maxSegmentID := binary.LittleEndian.Uint32(firstWord[:4])
	if maxSegmentID > maxStreamSegmentID {
		return nil, 0, fmt.Errorf("decode: segment id %d exceeds max segment count", maxSegmentID)
	}
	headerSize := ((uint64(maxSegmentID)+2)*4 + 7) &^ 7
	if headerSize > maxMessageSize || headerSize > math.MaxInt {
		return nil, 0, errors.New("decode: message too large")
	}

	header := make([]byte, int(headerSize))
	copy(header, firstWord[:])
	if _, err := io.ReadFull(decoder.reader, header[capnpWordSize:]); err != nil {
		return nil, 0, fmt.Errorf("decode: read header: %w", err)
	}

	var bodySize uint64
	for segmentID := uint32(0); segmentID <= maxSegmentID; segmentID++ {
		words := binary.LittleEndian.Uint32(header[4+segmentID*4:])
		segmentSize := uint64(words) * capnpWordSize
		if segmentSize > maxMessageSize-headerSize-bodySize {
			return nil, 0, errors.New("decode: message too large")
		}
		bodySize += segmentSize
	}
	frameSize := headerSize + bodySize
	if frameSize > math.MaxInt {
		return nil, 0, errors.New("decode: message too large")
	}
	if decoder.budget != nil {
		if err := decoder.budget.acquire(ctx, frameSize); err != nil {
			return nil, 0, err
		}
	}

	frame := make([]byte, int(frameSize))
	copy(frame, header)
	if _, err := io.ReadFull(decoder.reader, frame[headerSize:]); err != nil {
		if decoder.budget != nil {
			decoder.budget.release(frameSize)
		}
		return nil, 0, fmt.Errorf("decode: read segments: %w", err)
	}
	return frame, frameSize, nil
}
