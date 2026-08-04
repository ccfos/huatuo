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

// IO health keeps MD and kernel sources independent while sharing bounded
// evidence and persistence pipelines across kernel-session retries.
package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	ioHealthRestartWait      = 10
	ioHealthEventMap         = "health_events"
	ioHealthPerfBufferBytes  = 8192
	ioHealthBPFRetryInterval = ioHealthRestartWait * time.Second
	ioHealthMDRetryInterval  = ioHealthRestartWait * time.Second
)

var errIOHealthAttachRetry = errors.New("retry incomplete IO health hook set")

type ioHealthBPFLoader func(string, map[string]any) (bpf.BPF, error)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/io_health.c -o $BPF_DIR/io_health.o

func (c *ioHealthCollector) Start(ctx context.Context) error {
	return c.start(ctx, bpf.LoadBPF, ioHealthBPFRetryInterval)
}

func (c *ioHealthCollector) start(
	ctx context.Context,
	loadBPF ioHealthBPFLoader,
	retryInterval time.Duration,
) error {
	childCtx, cancel := context.WithCancel(ctx)
	eventWriter := newIOHealthEventWriter(
		c.saveEvent,
		c.incrementPersistenceFailure,
		ioHealthPersistenceQueueCapacity,
		ioHealthPersistenceTimeout,
	)
	eventWriter.Start(childCtx)
	c.setEventSubmitter(eventWriter.Submit)
	worker := iohealth.NewEvidenceWorker(iohealth.EvidenceWorkerOptions{
		OnResult:         c.handleEvidenceResult,
		ValidateIdentity: c.resolver.evidenceTargetCurrent,
	})
	worker.Start(childCtx)

	mdRetry := time.NewTicker(ioHealthMDRetryInterval)
	var consumers sync.WaitGroup
	consumers.Add(1)
	go func() {
		defer consumers.Done()
		c.superviseMDWatcher(childCtx, worker, mdRetry.C)
	}()

	defer func() {
		cancel()
		mdRetry.Stop()
		consumers.Wait()
		worker.Wait()
		eventWriter.Wait()
		c.setEventSubmitter(nil)
	}()

	for {
		attached, retryable, err := c.runBPFSession(
			childCtx,
			worker,
			loadBPF,
			retryInterval,
		)
		if ctx.Err() != nil {
			if retryable {
				return nil
			}
			return err
		}
		if err != nil {
			if !retryable {
				log.Warnf("io_health: kernel event source disabled: %v", err)
				<-ctx.Done()
				return nil
			}
			log.Warnf("io_health: kernel event source failed: %v; will retry", err)
			if errors.Is(err, errIOHealthAttachRetry) {
				continue
			}
			if !waitIOHealthRetryAfter(ctx, retryInterval) {
				return nil
			}
			continue
		}
		if attached == 0 {
			log.Warnf(
				"io_health: no kernel health hook is available; MD monitoring remains active",
			)
			<-ctx.Done()
			return nil
		}
		if !waitIOHealthRetryAfter(ctx, retryInterval) {
			return nil
		}
	}
}

func (c *ioHealthCollector) runBPFSession(
	ctx context.Context,
	worker ioHealthEvidenceSubmitter,
	loadBPF ioHealthBPFLoader,
	attachRetryInterval time.Duration,
) (attached int, retryable bool, retErr error) {
	states, quietMask := loadIOHealthKernelEnums()
	object, err := loadBPF("io_health.o", map[string]any{
		"io_health_rqf_quiet_mask": quietMask,
	})
	if err != nil {
		return 0, true, fmt.Errorf("load BPF: %w", err)
	}
	c.nvmeStates = states
	defer func() {
		if err := closeIOHealthBPF(ctx, object, attachRetryInterval); err != nil {
			retryable = false
			retErr = errors.Join(retErr, fmt.Errorf("close BPF: %w", err))
		}
	}()

	readerCtx, cancelReader := context.WithCancel(ctx)
	defer cancelReader()
	reader, err := object.EventPipeByName(
		readerCtx,
		ioHealthEventMap,
		ioHealthPerfBufferBytes,
	)
	if err != nil {
		return 0, true, fmt.Errorf("open event pipe: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			retryable = false
			retErr = errors.Join(retErr, fmt.Errorf("close event pipe: %w", err))
		}
	}()

	var attachErr error
	attached, attachErr = attachIOHealthHooks(
		object,
		c.resolver.primeNVMeControllerNames,
	)
	if attachErr != nil {
		if attached == 0 {
			return 0, true, attachErr
		}
		retryTimer := time.AfterFunc(attachRetryInterval, cancelReader)
		defer retryTimer.Stop()
	}
	if attached == 0 {
		return 0, false, nil
	}

	for {
		var event ioHealthPerfEvent
		if err := reader.ReadInto(&event); err != nil {
			if ctx.Err() != nil {
				return attached, false, nil
			}
			if attachErr != nil && readerCtx.Err() != nil {
				return attached, true, errors.Join(
					errIOHealthAttachRetry,
					attachErr,
				)
			}
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return attached, false, nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.Warnf("io_health: lost perf event samples: %v", err)
				continue
			}
			return attached, true, fmt.Errorf("read event: %w", err)
		}
		c.handleKernelEvent(event, worker)
	}
}

// Retain the object until cleanup is confirmed. Cancellation returns unresolved
// cleanup errors to the collector owner.
func closeIOHealthBPF(ctx context.Context, object bpf.BPF, retryInterval time.Duration) error {
	for {
		err := object.Close()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}

		loaded, lookupErr := object.IsLoaded()
		if lookupErr == nil && !loaded {
			return nil
		}
		if lookupErr != nil {
			err = errors.Join(err, fmt.Errorf("confirm BPF cleanup: %w", lookupErr))
		}
		log.Warnf("io_health: BPF cleanup unconfirmed: %v; will retry", err)
		if !waitIOHealthRetryAfter(ctx, retryInterval) {
			return err
		}
	}
}

func waitIOHealthRetryAfter(ctx context.Context, interval time.Duration) bool {
	retry := time.NewTimer(interval)
	defer retry.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-retry.C:
		return true
	}
}

func (c *ioHealthCollector) superviseMDWatcher(
	ctx context.Context,
	worker ioHealthEvidenceSubmitter,
	retry <-chan time.Time,
) {
	for {
		watcher := c.newMDWatcher(c.procMDStatPath, c.sysBlockPath)
		if err := watcher.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warnf("io_health: start MD watcher: %v; will retry", err)
			if !waitIOHealthRetry(ctx, retry) {
				return
			}
			continue
		}

		err := c.consumeMDWatcher(ctx, watcher, worker)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			log.Warnf("io_health: MD watcher stopped; will retry")
		} else {
			log.Warnf("io_health: MD watcher failed: %v; will retry", err)
		}
		if !waitIOHealthRetry(ctx, retry) {
			return
		}
	}
}

func (c *ioHealthCollector) consumeMDWatcher(
	ctx context.Context,
	watcher ioHealthMDWatcher,
	worker ioHealthEvidenceSubmitter,
) error {
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- watcher.Wait()
	}()

	changes := watcher.Changes()
	for {
		select {
		case <-ctx.Done():
			<-waitResult
			return c.finishMDWatcher(ctx.Err(), changes, worker)
		case change, ok := <-changes:
			if ok {
				c.handleMDChange(change, worker)
			} else {
				changes = nil
			}
		case err := <-waitResult:
			return c.finishMDWatcher(err, changes, worker)
		}
	}
}

func (c *ioHealthCollector) finishMDWatcher(
	err error,
	changes <-chan iohealth.MDChange,
	worker ioHealthEvidenceSubmitter,
) error {
	for changes != nil {
		select {
		case change, ok := <-changes:
			if !ok {
				return err
			}
			c.handleMDChange(change, worker)
		default:
			return err
		}
	}
	return err
}

func waitIOHealthRetry(ctx context.Context, retry <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-retry:
		return true
	}
}
