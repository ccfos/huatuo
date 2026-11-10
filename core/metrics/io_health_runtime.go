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

// IO health keeps MD and kernel sources independent while retaining one
// evidence worker across kernel-session retries. Events are saved synchronously.
package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

const (
	ioHealthRestartWait      = 10
	ioHealthEventMap         = "health_events"
	ioHealthPerfBufferBytes  = 8192
	ioHealthBPFRetryInterval = ioHealthRestartWait * time.Second
	ioHealthMDRetryInterval  = ioHealthRestartWait * time.Second
)

type ioHealthBPFLoader func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error)

func ioHealthKernelEventCurrent(object bpf.BPF, expected uint64) bool {
	mapID := object.MapIDByName("io_health_topology")
	if mapID == 0 {
		return false
	}
	data, err := object.ReadMap(mapID, []byte{0, 0, 0, 0})
	if err != nil {
		log.WithError(err).Warn("io_health: skip kernel evidence: read disk topology")
		return false
	}
	var generation uint64
	if err := decodeBPFMapData(data, &generation); err != nil {
		log.WithError(err).Warn("io_health: skip kernel evidence: decode disk topology")
		return false
	}
	return generation == expected
}

func (c *ioHealthCollector) Start(ctx context.Context) error {
	return c.start(ctx, loadIOHealthBPF, ioHealthBPFRetryInterval)
}

func loadIOHealthBPF(name string, constants map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
	options, err := loadIOHealthHookOptions()
	if err != nil {
		return nil, nil, err
	}
	// The raw completion fallback needs the running kernel's status table.
	// Missing metadata must not disable errno-based or other health events.
	spec, err := btf.LoadKernelSpec()
	if err == nil {
		var stride, count uint32
		stride, count, err = ioHealthBlockErrorLayout(spec)
		if err == nil {
			var addresses map[string]symbol.KsymbolRange
			addresses, err = symbol.KsymbolSearchAddr([]string{"blk_errors"}, symbol.KsymbolLookupData)
			if err == nil && addresses["blk_errors"].Start != 0 {
				constants["io_health_blk_errors_addr"] = addresses["blk_errors"].Start
				constants["io_health_blk_errors_stride"] = stride
				constants["io_health_blk_errors_count"] = count
			}
		}
	}
	if err != nil {
		log.WithError(err).Debug("io_health: raw block status mapping unavailable")
	}
	object, err := bpf.LoadBPF(name, constants)
	return object, options, err
}

func (c *ioHealthCollector) start(
	ctx context.Context,
	loadBPF ioHealthBPFLoader,
	retryInterval time.Duration,
) error {
	childCtx, cancel := context.WithCancel(ctx)
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
	}()

	// A partial attach can already queue events. Each retry must own a fresh
	// object and reader rather than carry those events into another attempt.
	// A running session gets a fresh recovery budget; only consecutive setup
	// failures can exhaust the three attempts.
	var attached, failures int
	var err error
	for {
		var canRetry bool
		attached, canRetry, err = c.runBPFSession(
			childCtx,
			worker,
			loadBPF,
		)
		if err != nil && !canRetry {
			err = fmt.Errorf("%w: %w", types.ErrTracingStopped, err)
			break
		}
		if ctx.Err() != nil || err == nil ||
			errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			break
		}
		if attached != 0 {
			failures = 0
		} else {
			failures++
		}
		if failures == 3 {
			err = fmt.Errorf("kernel session setup failed after %d attempts: %w", failures, err)
			break
		}
		log.WithError(err).Warn("io_health: kernel event session failed; will retry")
		timer := time.NewTimer(retryInterval)
		canRetry = waitIOHealthRetry(childCtx, timer.C)
		timer.Stop()
		if !canRetry {
			break
		}
	}
	if ctx.Err() != nil {
		if errors.Is(err, types.ErrTracingStopped) {
			return err
		}
		return nil
	}
	if err != nil {
		log.WithError(err).Warn("io_health: kernel event source disabled")
	} else if attached == 0 {
		log.Warnf("io_health: no kernel health hook is available; MD monitoring remains active")
	}
	<-ctx.Done()
	return nil
}

func (c *ioHealthCollector) runBPFSession(
	ctx context.Context,
	worker ioHealthEvidenceSubmitter,
	loadBPF ioHealthBPFLoader,
) (attached int, retryable bool, retErr error) {
	states, quietMask := loadIOHealthKernelEnums()
	object, options, err := loadBPF("io_health.o", map[string]any{
		"io_health_rqf_quiet_mask": quietMask,
	})
	if err != nil {
		return 0, !errors.Is(err, types.ErrNotSupported) && !errors.Is(err, types.ErrTracingStopped), fmt.Errorf("load BPF: %w", err)
	}
	c.nvmeStates = states
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

	err = attachIOHealthHooks(
		object,
		options,
		c.resolver.primeNVMeControllerNames,
	)
	if err != nil {
		return attached, !errors.Is(err, types.ErrNotSupported) &&
			!errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES), err
	}
	attached = len(options)
	if attached == 0 {
		return 0, false, nil
	}
	c.mu.Lock()
	c.freezeObject = object
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.freezeObject = nil
		c.mu.Unlock()
	}()

	for {
		var event abi.IOHealthEvent
		if err := reader.ReadInto(&event); err != nil {
			if ctx.Err() != nil {
				return attached, false, nil
			}
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return attached, false, nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("io_health: lost perf event samples")
				continue
			}
			return attached, true, fmt.Errorf("read event: %w", err)
		}
		c.handleKernelEvent(event, worker, func() bool {
			return ioHealthKernelEventCurrent(object, event.TopologyGeneration)
		})
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
			log.WithError(err).Warn("io_health: start MD watcher; will retry")
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
			log.WithError(err).Warn("io_health: MD watcher failed; will retry")
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
