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

package retransmit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
)

// readRetransmitEvent retries sample loss without transferring ownership of dst.
// A successful read is preserved even if cancellation happens concurrently.
func readRetransmitEvent(
	ctx context.Context,
	readInto func(*abi.TCPRetransmitEvent) error,
	dst *retransmitEvent,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst.record = abi.TCPRetransmitEvent{}
		if err := readInto(&dst.record); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).WithField("event", "TCP retransmit").Warn("perf event samples lost")
				continue
			}
			return fmt.Errorf("read TCP retransmit event: %w", err)
		}
		dst.observedAt = time.Now()
		return nil
	}
}

func writeRetransmitEvents(
	ctx context.Context,
	readInto func(*abi.TCPRetransmitEvent) error,
	sink writer,
	sourceType string,
) error {
	var event retransmitEvent
	for {
		if err := readRetransmitEvent(ctx, readInto, &event); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		tracing, err := event.tracing(sourceType)
		if err != nil {
			return err
		}
		if err := sink.Write(tracing); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}

func readRetransmitEvents(
	ctx context.Context,
	readInto func(*abi.TCPRetransmitEvent) error,
	events chan<- *retransmitEvent,
) error {
	for {
		// The receiver may retain this event while subsequent records are read.
		event := new(retransmitEvent)
		if err := readRetransmitEvent(ctx, readInto, event); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case events <- event:
		case <-ctx.Done():
			return nil
		}
	}
}

func readDropwatchEvents(
	ctx context.Context,
	read func(*abi.DropwatchPacketEvent) error,
	events chan<- *dropEvent,
) error {
	var record abi.DropwatchPacketEvent
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := read(&record); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("dropwatch perf event samples lost")
				continue
			}
			return fmt.Errorf("read dropwatch event: %w", err)
		}
		event, parseErr := dropEventFromRecord(&record)
		if parseErr != nil {
			if event == nil {
				return parseErr
			}
			log.WithError(parseErr).Debug("parse embedded dropwatch packet")
		}
		select {
		case events <- event:
		case <-ctx.Done():
			return nil
		}
	}
}
