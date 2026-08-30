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

// This file owns throttle collection boundaries and recovery. Shared IO
// helpers decode the cumulative CSS/device counters and attribute intervals.

package collector

import (
	"context"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	throtlDelayedCountName = "delayed_io_count"
	throtlAverageWaitName  = "average_wait_ms"
	throtlDelayedCountHelp = "Number of blk-throttle delayed IO episodes " +
		"released between consecutive successful blk_throtl collections."
	throtlAverageWaitHelp = "Average blk-throttle queue wait in milliseconds " +
		"for delayed IO episodes released between consecutive successful " +
		"blk_throtl collections."
)

var throtlWaitMetrics = ioControlWaitMetrics{
	countName: throtlDelayedCountName, countHelp: throtlDelayedCountHelp,
	averageName: throtlAverageWaitName, averageHelp: throtlAverageWaitHelp,
}

var _ metric.Collector = (*throtlTracing)(nil)

func (c *throtlTracing) Update() ([]*metric.Data, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	session := c.session
	if session == nil {
		return nil, metric.ErrNoData
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}
	if err := checkIOControlDeviceChanges(session.object); err != nil {
		return nil, session.collectionFailure(err)
	}

	current, err := captureIOControlWaitSnapshot(session.object,
		&session.hashReader, throtlWaitAggregateMap, session.possibleCPUs)
	if err != nil {
		return nil, session.collectionFailure(fmt.Errorf("capture blk_throtl counters: %w", err))
	}
	status, err := readIOControlStatus(session.object)
	if err != nil {
		return nil, session.collectionFailure(fmt.Errorf("read blk_throtl BPF health: %w", err))
	}
	changed := status.DevicesChanged != 0
	if !changed {
		if err := throtlFailure(status.Failure); err != nil {
			return nil, session.collectionFailure(err)
		}
	}
	rebaseline := session.previous == nil
	var metrics []*metric.Data
	if !rebaseline || changed {
		previous := session.previous
		if changed {
			// Recreated counters have no interval delta. Keep their series
			// visible as zero while the complete session is replaced.
			previous = current
		}
		containers := ioControlQueryContainers(session.containerSource)
		disks, err := readIOControlDiskNames()
		if err != nil {
			return nil, session.collectionFailure(err)
		}
		metrics, err = buildIOControlWaitMetrics(
			previous,
			current,
			pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO),
			disks,
			throtlWaitMetrics,
		)
		if err != nil {
			return nil, session.collectionFailure(err)
		}
	}
	if changed {
		return metrics, session.collectionFailure(errIOControlDevicesChanged)
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	session.previous = current
	if rebaseline {
		return nil, fmt.Errorf("%w: blk_throtl baseline recovered", metric.ErrNoData)
	}
	return metrics, nil
}

func (s *throtlSession) checkBreaker() error {
	if s.breaker == nil || s.breaker.Err() == nil {
		return nil
	}
	return fmt.Errorf("blk_throtl BPF session ended: %w", context.Cause(s.breaker))
}

func (s *throtlSession) collectionFailure(cause error) error {
	if errors.Is(cause, types.ErrTracingStopped) {
		// Device retirement can cause a concurrent map failure. Re-read the
		// sticky flag so that transition restarts the session.
		if err := checkIOControlDeviceChanges(s.object); err != nil {
			cause = err
		}
	}
	if errors.Is(cause, errIOControlDevicesChanged) ||
		errors.Is(cause, types.ErrTracingStopped) {
		if s.cancel != nil {
			s.cancel(cause)
		}
	} else {
		// A failed interval cannot be subtracted from the next valid snapshot.
		s.previous = nil
	}
	return cause
}
