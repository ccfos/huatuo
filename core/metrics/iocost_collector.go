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

// IOCOST captures cumulative CPU counters and commits each attributed interval
// only while its BPF session remains healthy.
package collector

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	ioCostSnapshotAttempts = 3

	ioCostWaitCountName   = "waitq_io_count"
	ioCostAverageWaitName = "average_wait_ms"
	ioCostWaitCountHelp   = "Number of READ/WRITE I/O requests whose departure " +
		"from the IOCOST wait queue was observed by the paired probes between " +
		"consecutive successful iocost collections."
	ioCostAverageWaitHelp = "Average IOCOST wait-queue residence time in " +
		"milliseconds for the READ/WRITE I/O requests counted by waitq_io_count."
)

var ioCostWaitMetrics = ioControlWaitMetrics{
	countName: ioCostWaitCountName, countHelp: ioCostWaitCountHelp,
	averageName: ioCostAverageWaitName, averageHelp: ioCostAverageWaitHelp,
}

var _ metric.Collector = (*iocostTracing)(nil)

func (collector *iocostTracing) Update() (metrics []*metric.Data, retErr error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()

	session := collector.session
	if session == nil || session.breaker.Err() != nil {
		return nil, metric.ErrNoData
	}
	defer func() {
		if retErr == nil || (errors.Is(retErr, metric.ErrNoData) &&
			!errors.Is(retErr, errIOControlDevicesChanged)) {
			return
		}
		session.previous = nil
		// The status words are independent. Resolve topology again before
		// treating an accounting error during teardown as terminal.
		if errors.Is(retErr, types.ErrTracingStopped) {
			status, err := readIOControlStatus(session.object)
			if err != nil {
				metrics, retErr = nil, err
			} else if status.DevicesChanged != 0 {
				metrics, retErr = nil, errIOControlDevicesChanged
			}
		}
		if errors.Is(retErr, types.ErrTracingStopped) ||
			errors.Is(retErr, errIOControlDevicesChanged) {
			session.cancel(retErr)
		}
	}()

	status, err := readIOControlStatus(session.object)
	if err != nil {
		return nil, err
	}
	if status.DevicesChanged != 0 {
		return nil, errIOControlDevicesChanged
	}
	if err := ioCostFailure(status.Failure); err != nil {
		return nil, err
	}
	current, err := session.captureRawSnapshot()
	if err != nil {
		return nil, err
	}
	status, err = readIOControlStatus(session.object)
	if err != nil {
		return nil, err
	}
	changed := status.DevicesChanged != 0
	if !changed {
		if err := ioCostFailure(status.Failure); err != nil {
			return nil, err
		}
	}
	recovering := session.previous == nil
	if !recovering || changed {
		previous := session.previous
		if changed {
			// A recreated device can reset either packed field. Subtract the
			// snapshot from itself to retain labels without a false delta.
			previous = current
		}
		containers := ioControlQueryContainers(session.containerSource)
		disks, err := readIOControlDiskNames()
		if err != nil {
			return nil, err
		}
		metrics, err = buildIOControlWaitMetrics(previous, current,
			pod.BuildCssContainers(containers, subsystem.SubsystemBlkIO), disks, ioCostWaitMetrics)
		if err != nil {
			return nil, err
		}
	}
	if changed {
		return metrics, errIOControlDevicesChanged
	}
	if err := session.checkBreaker(); err != nil {
		return nil, err
	}

	session.previous = current
	if recovering {
		return nil, fmt.Errorf("%w: iocost baseline recovered", metric.ErrNoData)
	}
	return metrics, nil
}

// Capture leaves the baseline unchanged. Update owns error classification and
// cancellation; duplicate HASH traversal retries only this map.
func (session *ioCostSession) captureRawSnapshot() (ioControlWaitSnapshot, error) {
	var snapshot ioControlWaitSnapshot
	var err error
	for attempt := 0; attempt < ioCostSnapshotAttempts; attempt++ {
		snapshot, err = captureIOControlWaitSnapshot(session.object,
			&session.hashReader, ioCostWaitAggregateMap, session.possibleCPUs)
		if !errors.Is(err, errIOControlSnapshotUnstable) {
			return snapshot, err
		}
	}
	return nil, fmt.Errorf("capture stable iocost snapshot after %d attempts: %w",
		ioCostSnapshotAttempts, err)
}

func (session *ioCostSession) checkBreaker() error {
	if err := session.breaker.Err(); err != nil {
		return fmt.Errorf("iocost BPF session ended: %w", err)
	}
	return nil
}
