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
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
)

func mainAction(c *cli.Context) error {
	reasonNames = loadDropReasonNames()

	duration := c.Int(cliFlagDuration)
	outputFmt := c.String(cliFlagOutput)
	sourceTypes := c.String(cliFlagSourceTypes)

	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: duration}); err != nil {
		return fmt.Errorf("dropwatch: init bpf: %w", err)
	}
	defer bpf.Shutdown()

	netdevFilterMode, devIfindexes, err := parseNetdevFilterFlags(c.String(cliFlagDevice), c.String(cliFlagDeviceExcluded))
	if err != nil {
		return fmt.Errorf("dropwatch: %w", err)
	}

	maxEventsPerSecond := c.Uint64(cliFlagMaxEventsPerSecond)

	bpfObj, err := loadDropwatchBPFWithFilter(c.String(cliFlagBpfPath), c.String(cliFlagFilter), netdevFilterMode, maxEventsPerSecond)
	if err != nil {
		return fmt.Errorf("dropwatch: load bpf: %w", err)
	}
	defer bpfObj.Close()

	if err := applyDeviceFilter(bpfObj, netdevFilterMode, devIfindexes); err != nil {
		return fmt.Errorf("dropwatch: device filter map: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if duration > 0 {
		var dcancel context.CancelFunc
		runCtx, dcancel = context.WithTimeout(runCtx, time.Duration(duration)*time.Second)
		defer dcancel()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGINT, unix.SIGTERM)
	defer signal.Stop(sig)

	go func() {
		select {
		case <-sig:
			cancel()
		case <-runCtx.Done():
		}
	}()

	if maxEventsPerSecond > 0 {
		rlReader, err := eventRateLimiter.OpenEventPipe(runCtx, bpfObj)
		if err != nil {
			return err
		}
		defer rlReader.Close()

		go eventRateLimiter.ReadEvents(runCtx, rlReader, maxEventsPerSecond)
	}

	reader, err := bpfObj.AttachAndEventPipe(runCtx, "perf_events", 8192)
	if err != nil {
		return fmt.Errorf("dropwatch: attach: %w", err)
	}
	defer reader.Close()

	bpfObj.DetachOnContextDone(runCtx, cancel)

	sink, sinkCleanup, err := newWriter(&writerOption{
		outputFmt: outputFmt,
		sockPath:  c.String(cliFlagOutputStorage),
		toolName:  dropwatchToolName,
		version:   versionInfo.Version,
		taskID:    c.String(cliFlagTaskID),
	})
	if err != nil {
		return err
	}
	defer sinkCleanup()

	var ev abi.DropwatchPacketEvent

	for {
		if runCtx.Err() != nil {
			return nil
		}

		if err := reader.ReadInto(&ev); err != nil {
			if runCtx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("lost BPF perf event samples")
				continue
			}

			log.Errorf("dropwatch: read: %v", err)

			continue
		}

		event := formatEvent(&ev)
		event.Source = sourceTypes
		if err := sink.Write(event); err != nil {
			log.Errorf("dropwatch: send event: %v", err)
			return nil
		}
	}
}
