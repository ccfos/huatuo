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

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	embeddedHardwareProgramSection = "raw_tracepoint/devlink_trap_report"
	embeddedPerfStatusMapName      = "dropwatch_perf_stats"
	dropwatchPerfBufferSize        = 8192
)

type dropwatchSource struct {
	object         bpf.BPF
	reader         bpf.PerfEventReader
	perfStatusMap  uint32
	previousStatus types.DropwatchPerfStatus
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

	reader, err := object.EventPipeByName(
		ctx,
		"perf_events",
		dropwatchPerfBufferSize,
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open embedded dropwatch event pipe: %w", err),
			object.Close(),
		)
	}
	perfStatusMap := object.MapIDByName(embeddedPerfStatusMapName)
	if perfStatusMap == 0 {
		return nil, errors.Join(
			fmt.Errorf("embedded dropwatch BPF map %q not found", embeddedPerfStatusMapName),
			reader.Close(),
			object.Close(),
		)
	}
	if err := object.Attach(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("attach embedded dropwatch probes: %w", err),
			reader.Close(),
			object.Close(),
		)
	}

	return &dropwatchSource{
		object:        object,
		reader:        reader,
		perfStatusMap: perfStatusMap,
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
	if len(raw) == 0 || len(raw)%abi.DropwatchPerfStatsSize != 0 {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"decode embedded dropwatch BPF map %q: value size %d is not a positive multiple of %d",
			embeddedPerfStatusMapName,
			len(raw),
			abi.DropwatchPerfStatsSize,
		)
	}

	var status types.DropwatchPerfStatus
	for offset := 0; offset < len(raw); offset += abi.DropwatchPerfStatsSize {
		var cpuStatus abi.DropwatchPerfStats
		if err := binary.Read(
			bytes.NewReader(raw[offset:offset+abi.DropwatchPerfStatsSize]),
			binary.NativeEndian,
			&cpuStatus,
		); err != nil {
			return types.DropwatchPerfStatus{}, fmt.Errorf(
				"decode embedded dropwatch BPF map %q: %w",
				embeddedPerfStatusMapName,
				err,
			)
		}
		if math.MaxUint64-status.PerfLost < cpuStatus.PerfLost {
			return types.DropwatchPerfStatus{}, fmt.Errorf(
				"decode embedded dropwatch BPF map %q: perf_lost overflow",
				embeddedPerfStatusMapName,
			)
		}
		status.PerfLost += cpuStatus.PerfLost
		if math.MaxUint64-status.RateLimited < cpuStatus.RateLimited {
			return types.DropwatchPerfStatus{}, fmt.Errorf(
				"decode embedded dropwatch BPF map %q: rate_limited overflow",
				embeddedPerfStatusMapName,
			)
		}
		status.RateLimited += cpuStatus.RateLimited
	}

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
