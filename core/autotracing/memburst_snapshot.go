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

package autotracing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

// Bound each capture while following the configured process ranking size.
const (
	burstProcessSnapshotBudget = 2 * time.Second
	maxBurstSnapshotProcesses  = 10
	maxBurstTracingDataBytes   = 2 << 20
	// Bound ranking serialization before applying the whole-data byte budget.
	maxBurstPersistedProcesses = 1000
)

type burstProcessSnapshot struct {
	identity      memsnapshot.ProcessInstanceID
	PID           int32                      `json:"pid"`
	ProcessName   string                     `json:"process_name"`
	Language      memsnapshot.Language       `json:"language"`
	Snapshot      *memsnapshot.Snapshot      `json:"snapshot"`
	ProcessMemory *memsnapshot.ProcessMemory `json:"process_memory,omitempty"`
}
type burstSnapshotJob struct {
	processes []*processMemInfo
	started   time.Time
}

func collectBurstSnapshots(ctx context.Context, processes []*processMemInfo, topN int) []burstProcessSnapshot {
	processes = processes[:min(len(processes), max(topN, 0), maxBurstSnapshotProcesses)]
	batchCtx, cancel := context.WithTimeout(ctx, time.Duration(len(processes))*burstProcessSnapshotBudget)
	defer cancel()
	out := make([]burstProcessSnapshot, 0, len(processes))
	for _, process := range processes {
		if process == nil {
			continue
		}
		record := burstProcessSnapshot{
			PID:         process.PID,
			ProcessName: process.ProcessName,
			Language:    memsnapshot.LanguageUnknown,
			identity:    process.identity,
		}
		result, err := captureBurstProcess(batchCtx, process)
		if err != nil {
			record.Snapshot = burstCaptureFailure(err)
		} else {
			record.Language, record.Snapshot, record.ProcessMemory = result.Language, result.Snapshot, result.ProcessMemory
		}
		out = append(out, record)
	}
	return out
}

// Capture errors bypass the collector result's output normalization.
func burstCaptureFailure(err error) *memsnapshot.Snapshot {
	snapshot := memsnapshot.Failed(err.Error())
	if limitErr := memsnapshot.LimitOutput(snapshot, 10); limitErr != nil {
		return memsnapshot.Failed("failed to normalize capture error")
	}
	return snapshot
}

func captureBurstProcess(ctx context.Context, process *processMemInfo) (*collector.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity := process.identity
	if identity.TGID != int(process.PID) {
		return nil, fmt.Errorf("process identity was not captured during ranking")
	}
	if err := memsnapshot.ValidateProcessInstanceID(identity); err != nil {
		return nil, err
	}
	result, err := collector.Snapshot(ctx, identity, collector.Options{MaxMemoryObjectEntries: 10, SnapshotTimeout: burstProcessSnapshotBudget})
	if err != nil {
		return nil, err
	}
	if err := memsnapshot.ValidateProcessInstanceID(identity); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *memBurstTracing) enqueueBurst(ctx context.Context, job *burstSnapshotJob) {
	select {
	case <-ctx.Done():
		c.saveBurst(job, nil, "snapshot canceled: "+ctx.Err().Error())
	case c.snapshotJobs <- job:
	default:
		// A full queue must not block periodic sampling or erase the basic burst event.
		c.saveBurst(job, nil, "snapshot queue is full")
	}
}

func (c *memBurstTracing) runBurstSnapshots(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	// Start closes the producer-owned queue before joining this worker. Drain
	// accepted jobs even after cancellation so the basic burst event survives.
	for job := range c.snapshotJobs {
		if err := ctx.Err(); err != nil {
			c.saveBurst(job, nil, "snapshot canceled: "+err.Error())
			continue
		}
		snapshots := collectBurstSnapshots(ctx, job.processes, c.snapshotProcessMaxNum)
		if err := ctx.Err(); err != nil {
			c.saveBurst(job, nil, "snapshot canceled: "+err.Error())
			continue
		}
		revalidateBurstSnapshots(snapshots)
		c.saveBurst(job, snapshots, "")
	}
}

func (*memBurstTracing) saveBurst(job *burstSnapshotJob, snapshots []burstProcessSnapshot, reason string) {
	data, err := boundedBurstTracingData(job.processes, snapshots, reason)
	if err != nil {
		log.WithError(err).Warn("encode memburst snapshot")
		return
	}
	if err := tracing.Save(&tracing.WriteRequest{
		TracerName: "memburst", ContainerID: "", StartedTimestamp: timeutil.Timestamp{Time: job.started},
		TracerData:    data,
		TracerRunType: types.TracerRunTypeAutotracing,
	}); err != nil {
		log.WithError(err).Warn("save memburst snapshot")
	}
}

// Recheck process identity immediately before persistence.
func revalidateBurstSnapshots(snapshots []burstProcessSnapshot) {
	for i := range snapshots {
		if err := memsnapshot.ValidateProcessInstanceID(snapshots[i].identity); err != nil {
			snapshots[i].Snapshot = burstCaptureFailure(err)
			snapshots[i].ProcessMemory = nil
		}
	}
}

// Bound the complete tracer_data, including JSON escaping and failure records.
// Prefer retaining the base ranking when snapshot payloads exhaust the budget.
func boundedBurstTracingData(processes []*processMemInfo, snapshots []burstProcessSnapshot, reason string) (*MemoryTracingData, error) {
	data := &MemoryTracingData{TopMemoryUsage: processes, ProcessSnapshots: snapshots, SnapshotReason: reason}
	markTruncated := func() { data.SnapshotReason = "memburst payload truncated to fit event budget" }
	if len(data.TopMemoryUsage) > maxBurstPersistedProcesses {
		data.TopMemoryUsage = data.TopMemoryUsage[:maxBurstPersistedProcesses]
		markTruncated()
	}
	if len(data.ProcessSnapshots) > maxBurstSnapshotProcesses {
		data.ProcessSnapshots = data.ProcessSnapshots[:maxBurstSnapshotProcesses]
		markTruncated()
	}
	for {
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxBurstTracingDataBytes {
			return data, nil
		}
		markTruncated()
		if len(data.ProcessSnapshots) > 0 {
			data.ProcessSnapshots = data.ProcessSnapshots[:len(data.ProcessSnapshots)-1]
		} else if len(data.TopMemoryUsage) > 0 {
			data.TopMemoryUsage = data.TopMemoryUsage[:len(data.TopMemoryUsage)-1]
		} else {
			return nil, fmt.Errorf("memburst event budget cannot hold metadata")
		}
	}
}
