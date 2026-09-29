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

// Package collector orchestrates runtime snapshots independently of their trigger.

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// Options uses the before-OOM budgets as defaults for zero-valued fields.
// SnapshotTimeout is cooperative; it cannot interrupt an in-flight syscall.
type Options struct {
	MaxMemoryObjectEntries int
	SnapshotTimeout        time.Duration
}

// Result carries runtime data without event or container metadata.
type Result struct {
	Process           memsnapshot.ProcessInstanceID
	Language          memsnapshot.Language
	SnapshotStartedAt time.Time
	Snapshot          *memsnapshot.Snapshot
	ProcessMemory     *memsnapshot.ProcessMemory
}

// Snapshot binds memory collection to an already selected process instance.
// Callers must validate the process instance before calling Snapshot.
// Provider failures become failed snapshots. Detection and output-processing
// failures, cancellation and invalid options return errors without a result.
func Snapshot(ctx context.Context, process memsnapshot.ProcessInstanceID,
	options Options,
) (*Result, error) {
	pid := process.TGID
	options.setDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshotStartedAt := time.Now().UTC()

	language, err := memsnapshot.DetectLanguage(pid)
	if err != nil {
		return nil, fmt.Errorf("detect process runtime: %w", err)
	}

	snapshotCtx, cancelSnapshot := context.WithTimeout(ctx, options.SnapshotTimeout)
	snapshot := snapshotProvider(snapshotCtx, newProvider(language), process, options.MaxMemoryObjectEntries)
	cancelSnapshot()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := memsnapshot.LimitOutput(snapshot, options.MaxMemoryObjectEntries); err != nil {
		return nil, fmt.Errorf("limit runtime snapshot output: %w", err)
	}
	processMemory := readProcessMemory(pid)
	if err := memsnapshot.ValidateProcessInstanceID(process); err != nil {
		return nil, fmt.Errorf("validate process after snapshot: %w", err)
	}
	return &Result{
		Process:           process,
		Language:          language,
		SnapshotStartedAt: snapshotStartedAt,
		Snapshot:          snapshot,
		ProcessMemory: processMemory,
	}, nil
}
