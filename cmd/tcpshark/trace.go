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

const (
	bpfProgramTCPRetransmitSKB    = "retrans_skb"
	bpfProgramTCPRetransmitSynack = "retrans_synack"
	bpfProgramTCPRetransmitTLP    = "retrans_tlp"
)

func mainAction(c *cli.Context) error {
	switch c.String(cliFlagMode) {
	case modeRetransmit:
		return runRetransmit(c)
	default:
		return fmt.Errorf("tcpshark: unsupported mode %q", c.String(cliFlagMode))
	}
}

func runRetransmit(c *cli.Context) error {
	duration := c.Int(cliFlagDuration)
	outputFmt := c.String(cliFlagOutput)
	sourceTypes := c.String(cliFlagSourceTypes)
	maxEventsPerSecond := c.Uint64(cliFlagMaxEventsPerSecond)

	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: duration}); err != nil {
		return fmt.Errorf("tcpshark: init bpf: %w", err)
	}
	defer bpf.Shutdown()

	bpfObj, err := loadTCPRetransmitBPFWithFilter(
		c.String(cliFlagBpfPath),
		c.String(cliFlagFilter),
		maxEventsPerSecond,
	)
	if err != nil {
		return fmt.Errorf("tcpshark: load retransmit bpf: %w", err)
	}
	defer bpfObj.Close()

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

	reader, err := attachRetransmitPrograms(
		runCtx,
		bpfObj,
		c.Bool(cliFlagEnableTLP),
	)
	if err != nil {
		return fmt.Errorf("tcpshark: attach retransmit probes: %w", err)
	}
	defer reader.Close()

	bpfObj.DetachOnContextDone(runCtx, cancel)

	sink, sinkCleanup, err := newWriter(&writerOption{
		outputFmt: outputFmt,
		sockPath:  c.String(cliFlagOutputStorage),
		toolName:  tcpSharkToolName,
		version:   versionInfo.Version,
		taskID:    c.String(cliFlagTaskID),
	})
	if err != nil {
		return err
	}
	defer sinkCleanup()

	for {
		if runCtx.Err() != nil {
			return nil
		}

		var ev abi.TCPRetransmitEvent
		if err := reader.ReadInto(&ev); err != nil {
			if runCtx.Err() != nil {
				return nil
			}
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				log.WithError(err).Warn("lost BPF perf event samples")
				continue
			}
			log.Errorf("tcpshark: read retransmit event: %v", err)
			continue
		}

		event := formatEvent(&ev)
		event.Source = sourceTypes
		if err := sink.Write(event); err != nil {
			log.Errorf("tcpshark: send retransmit event: %v", err)
			return nil
		}
	}
}

func attachRetransmitPrograms(
	ctx context.Context,
	bpfObj bpf.BPF,
	isTLPEnabled bool,
) (bpf.PerfEventReader, error) {
	reader, err := bpfObj.EventPipeByName(ctx, "perf_events", 8192)
	if err != nil {
		return nil, err
	}

	if err := bpfObj.AttachWithOptions(retransmitAttachOptions(isTLPEnabled)); err != nil {
		reader.Close()
		return nil, err
	}
	return reader, nil
}

func retransmitAttachOptions(isTLPEnabled bool) []bpf.AttachOption {
	options := []bpf.AttachOption{
		{
			ProgramName: bpfProgramTCPRetransmitSKB,
			Symbol:      "tcp/tcp_retransmit_skb",
		},
		{
			ProgramName: bpfProgramTCPRetransmitSynack,
			Symbol:      "tcp/tcp_retransmit_synack",
		},
	}
	if isTLPEnabled {
		options = append(options, bpf.AttachOption{
			ProgramName: bpfProgramTCPRetransmitTLP,
			Symbol:      "tcp_send_loss_probe",
		})
	}

	return options
}
