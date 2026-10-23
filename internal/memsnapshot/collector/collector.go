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
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// ErrCaptureBusy lets triggers retain their event without concurrent external scans.
var (
	ErrCaptureBusy = errors.New("another memory snapshot is in progress")
	captureSlot    = make(chan struct{}, 1)
)

// Options uses the before-OOM budgets as defaults for zero-valued fields.
// SnapshotTimeout is cooperative; it cannot interrupt an in-flight syscall.
type Options struct {
	MaxMemoryObjectEntries int
	SnapshotTimeout        time.Duration
	// WaitForCapture queues threshold captures behind the current scan.
	WaitForCapture bool
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
// Detection and provider failures become failed snapshots, retaining process memory.
// Output-processing failures, cancellation and invalid options return no result.
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
	if err := acquireCapture(ctx, options); err != nil {
		return nil, err
	}
	defer func() { <-captureSlot }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshotStartedAt := time.Now().UTC()

	language, detectionErr := memsnapshot.DetectLanguage(pid)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var snapshot *memsnapshot.Snapshot
	if detectionErr != nil {
		if err := memsnapshot.ValidateProcessInstanceID(process); err != nil {
			return nil, fmt.Errorf("detect process runtime: %w; validate process: %w", detectionErr, err)
		}
		language = memsnapshot.LanguageUnknown
		snapshot = memsnapshot.Failed("detect process runtime: " + detectionErr.Error())
	} else {
		snapshotCtx, cancelSnapshot := context.WithTimeout(ctx, options.SnapshotTimeout)
		snapshot = snapshotProvider(snapshotCtx, newProvider(language), process, options.MaxMemoryObjectEntries)
		cancelSnapshot()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := memsnapshot.LimitOutput(snapshot, options.MaxMemoryObjectEntries); err != nil {
		return nil, fmt.Errorf("limit runtime snapshot output: %w", err)
	}
	return &Result{
		Process:           process,
		Language:          language,
		SnapshotStartedAt: snapshotStartedAt,
		Snapshot:          snapshot,
		ProcessMemory:     readProcessMemory(pid),
	}, nil
}

func acquireCapture(ctx context.Context, options Options) error {
	if !options.WaitForCapture {
		select {
		case captureSlot <- struct{}{}:
			return nil
		default:
			return ErrCaptureBusy
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, options.SnapshotTimeout)
	defer cancel()
	select {
	case captureSlot <- struct{}{}:
		return nil
	case <-waitCtx.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: wait budget exceeded", ErrCaptureBusy)
	}
}
