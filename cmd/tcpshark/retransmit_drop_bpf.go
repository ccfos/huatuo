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

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	embeddedHardwareProgramSection = "raw_tracepoint/devlink_trap_report"
	embeddedPerfStatusMapName      = "bpf_perf_out_dropwatch"
	embeddedRateLimitStateMapName  = "bpf_rlimit_dropwatch"
	dropwatchPerfBufferSize        = 8192
)

type dropwatchSource struct {
	object            bpf.BPF
	reader            bpf.PerfEventReader
	perfStatusMap     uint32
	rateLimitStateMap uint32
	previousStatus    types.DropwatchPerfStatus
	lostSamples       atomic.Uint64
}

func loadEmbeddedDropwatchBPF(
	bpfPath string,
	filterExpression string,
	maxEventsPerSecond uint64,
) (bpf.BPF, error) {
	limiter := bpf.NewRateLimiter("dropwatch", maxEventsPerSecond)
	constants := limiter.Constants(map[string]any{"filter_dev_mode": uint32(0)})
	object, err := loadFilteredBPFObject(
		bpfPath,
		filterExpression,
		constants,
		embeddedHardwareProgramSection,
	)
	if err != nil {
		return nil, fmt.Errorf("load embedded dropwatch BPF object %q: %w", bpfPath, err)
	}
	return object, nil
}

func openDropwatchSource(
	ctx context.Context,
	bpfPath string,
	filterExpression string,
	maxEventsPerSecond uint64,
) (*dropwatchSource, error) {
	object, err := loadEmbeddedDropwatchBPF(
		bpfPath,
		filterExpression,
		maxEventsPerSecond,
	)
	if err != nil {
		return nil, err
	}

	perfStatusMap := object.MapIDByName(embeddedPerfStatusMapName)
	rateLimitStateMap := object.MapIDByName(embeddedRateLimitStateMapName)
	if perfStatusMap == 0 || rateLimitStateMap == 0 {
		return nil, errors.Join(
			fmt.Errorf(
				"embedded dropwatch BPF maps %q, %q not found",
				embeddedPerfStatusMapName,
				embeddedRateLimitStateMapName,
			),
			object.Close(),
		)
	}

	// AttachAndEventPipe closes the reader itself and detaches any
	// already-attached links when attach fails, so object.Close() alone is
	// sufficient on every error path.
	reader, err := object.AttachAndEventPipe(
		ctx,
		"perf_events",
		dropwatchPerfBufferSize,
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("attach embedded dropwatch probes: %w", err),
			object.Close(),
		)
	}

	return &dropwatchSource{
		object:            object,
		reader:            reader,
		perfStatusMap:     perfStatusMap,
		rateLimitStateMap: rateLimitStateMap,
	}, nil
}

func (s *dropwatchSource) readEvents(
	ctx context.Context,
	events chan<- *dropEvent,
) error {
	return readPerfEvents[abi.DropwatchPacketEvent](
		ctx,
		s.reader,
		"embedded dropwatch",
		func(count uint64) {
			s.lostSamples.Add(count)
		},
		func(record *abi.DropwatchPacketEvent) error {
			event, parseErr := dropEventFromRecord(record)
			if parseErr != nil {
				if event == nil {
					return parseErr
				}
				log.WithError(parseErr).Debug("parse embedded dropwatch packet")
			}
			if event == nil {
				return fmt.Errorf("convert embedded dropwatch event: no event")
			}

			select {
			case events <- event:
				return nil
			case <-ctx.Done():
				return nil
			}
		},
	)
}

func (s *dropwatchSource) readPerfStatus() (types.DropwatchPerfStatus, error) {
	key := make([]byte, 4)
	raw, err := s.object.ReadMap(s.perfStatusMap, key)
	if err != nil {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"read embedded dropwatch BPF map %q: %w",
			embeddedPerfStatusMapName,
			err,
		)
	}
	if len(raw) == 0 || len(raw)%abi.BPFPerfOutputStatsSize != 0 {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"decode embedded dropwatch BPF map %q: value size %d is not a positive multiple of %d",
			embeddedPerfStatusMapName,
			len(raw),
			abi.BPFPerfOutputStatsSize,
		)
	}

	var status types.DropwatchPerfStatus
	for offset := 0; offset < len(raw); offset += abi.BPFPerfOutputStatsSize {
		var cpuStatus abi.BPFPerfOutputStats
		if err := binary.Read(
			bytes.NewReader(raw[offset:offset+abi.BPFPerfOutputStatsSize]),
			binary.NativeEndian,
			&cpuStatus,
		); err != nil {
			return types.DropwatchPerfStatus{}, fmt.Errorf(
				"decode embedded dropwatch BPF map %q: %w",
				embeddedPerfStatusMapName,
				err,
			)
		}
		if math.MaxUint64-status.PerfLost < cpuStatus.Lost {
			return types.DropwatchPerfStatus{}, fmt.Errorf(
				"decode embedded dropwatch BPF map %q: perf_lost overflow",
				embeddedPerfStatusMapName,
			)
		}
		status.PerfLost += cpuStatus.Lost
	}

	raw, err = s.object.ReadMap(s.rateLimitStateMap, key)
	if err != nil {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"read embedded dropwatch BPF map %q: %w",
			embeddedRateLimitStateMapName,
			err,
		)
	}

	var state abi.BPFRatelimitEvent
	if err := binary.Read(bytes.NewReader(raw), binary.NativeEndian, &state); err != nil {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"decode embedded dropwatch BPF map %q: %w",
			embeddedRateLimitStateMapName,
			err,
		)
	}
	status.RateLimited = state.TotalMissed

	previous := s.previousStatus
	if status.PerfLost < previous.PerfLost {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"embedded dropwatch perf_lost regressed from %d to %d",
			previous.PerfLost,
			status.PerfLost,
		)
	}
	if status.RateLimited < previous.RateLimited {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"embedded dropwatch rate_limited regressed from %d to %d",
			previous.RateLimited,
			status.RateLimited,
		)
	}
	s.previousStatus = status
	return status, nil
}

func (s *dropwatchSource) close() error {
	detachErr := s.object.Detach()
	readerErr := s.reader.Close()
	objectErr := s.object.Close()
	return errors.Join(detachErr, readerErr, objectErr)
}
