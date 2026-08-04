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

package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/ioobserve/health"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"
)

const (
	ioHealthRestartWait      = 10
	ioHealthEventMap         = "health_events"
	ioHealthPerfBufferBytes  = 8192
	ioHealthBPFRetryInterval = ioHealthRestartWait * time.Second
	ioHealthMDRetryInterval  = ioHealthRestartWait * time.Second
)

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
	worker := health.NewEvidenceWorker(health.EvidenceWorkerOptions{
		OnResult: c.handleEvidenceResult,
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
	}()

	for {
		attached, retryable, err := c.runBPFSession(childCtx, worker, loadBPF)
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
) (attached int, retryable bool, retErr error) {
	object, err := loadBPF("io_health.o", nil)
	if err != nil {
		return 0, true, fmt.Errorf("load BPF: %w", err)
	}
	defer func() {
		if err := object.Close(); err != nil {
			retryable = false
			retErr = errors.Join(retErr, fmt.Errorf("close BPF: %w", err))
		}
	}()

	reader, err := object.EventPipeByName(
		ctx,
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

	attached = attachIOHealthHooks(
		object,
		c.resolver.primeNVMeControllerNames,
		ioHealthLegacyBlockCompleteSupported(ioHealthHostKernelRelease),
	)
	if attached == 0 {
		return 0, false, nil
	}

	for {
		var event ioHealthPerfEvent
		if err := reader.ReadInto(&event); err != nil {
			if ctx.Err() != nil || errors.Is(err, types.ErrExitByCancelCtx) {
				return attached, false, nil
			}
			return attached, true, fmt.Errorf("read event: %w", err)
		}
		c.handleKernelEvent(event, worker)
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
	changes <-chan health.MDChange,
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
