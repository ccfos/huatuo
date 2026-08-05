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
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/version"
)

func mainAction(c *cli.Context, versionInfo *version.Info) (returnErr error) {
	names := loadDropReasonNames()
	duration := c.Int(cliFlagDuration)

	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: duration}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	netdevFilterMode, devIfindexes, err := parseNetdevFilterFlags(
		c.String(cliFlagDevice), c.String(cliFlagDeviceExcluded),
	)
	if err != nil {
		return err
	}

	bpfLimiter := bpf.NewRateLimiter("dropwatch", c.Uint64(cliFlagMaxEventsPerSecond))
	bpfObj, err := loadDropwatchBPFWithFilter(
		c.String(cliFlagBpfPath), c.String(cliFlagFilter), netdevFilterMode, bpfLimiter,
	)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer func() {
		if err := bpfObj.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close bpf: %w", err))
		}
	}()

	if err := applyDeviceFilter(bpfObj, netdevFilterMode, devIfindexes); err != nil {
		return fmt.Errorf("apply device filter: %w", err)
	}

	runCtx, cancel := signal.NotifyContext(c.Context, unix.SIGINT, unix.SIGTERM)
	defer cancel()
	if duration > 0 {
		var durationCancel context.CancelFunc
		runCtx, durationCancel = context.WithTimeout(
			runCtx, time.Duration(duration)*time.Second,
		)
		defer durationCancel()
	}

	group, groupCtx := errgroup.WithContext(runCtx)
	if bpfLimiter.Enabled() {
		if err := bpfLimiter.OpenEventPipe(groupCtx, bpfObj); err != nil {
			return err
		}
		defer func() {
			if err := bpfLimiter.CloseEventPipe(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close rate limiter: %w", err))
			}
		}()
	}

	reader, err := bpfObj.AttachAndEventPipe(groupCtx, "perf_events", 8192)
	if err != nil {
		return fmt.Errorf("attach BPF programs: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close event pipe: %w", err))
		}
	}()
	bpfObj.DetachOnContextDone(runCtx, cancel)

	sink, sinkCleanup, err := newWriter(os.Stdout, &writerOptions{
		outputFormat: c.String(cliFlagOutput),
		socketPath:   c.String(cliFlagOutputStorage),
		toolName:     dropwatchToolName,
		version:      versionInfo.Version,
		taskID:       c.String(cliFlagTaskID),
	})
	if err != nil {
		return err
	}

	if bpfLimiter.Enabled() {
		group.Go(func() error { return bpfLimiter.ReadEvents(groupCtx) })
	}
	group.Go(func() error {
		return streamDropwatchEvents(groupCtx, reader, sink, names, c.String(cliFlagSourceTypes))
	})

	streamErr := group.Wait()
	if err := sinkCleanup(); err != nil {
		streamErr = errors.Join(streamErr, fmt.Errorf("close event sink: %w", err))
	}
	return streamErr
}

func streamDropwatchEvents(
	ctx context.Context,
	reader bpf.PerfEventReader,
	sink writer,
	names dropReasonNames,
	sourceType string,
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		var ev abi.DropwatchPacketEvent
		if err := reader.ReadInto(&ev); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("perf event samples lost")
				continue
			}
			return fmt.Errorf("read event: %w", err)
		}

		if err := sink.Write(formatEvent(&ev, names, sourceType)); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
}
