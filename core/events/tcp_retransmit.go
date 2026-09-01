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

package events

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/internal/pcapfilter"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
	"github.com/ccfos/huatuo/pkg/types"
)

type tcpRetransmitTracing struct{}

const (
	tcpRetransmitTracerName = "tcp_retransmit"
	tcpSharkToolName        = "tcpshark"
)

func init() {
	tracing.RegisterEventTracing(tcpRetransmitTracerName, newTCPRetransmit)
	toolstream.RegisterDefault[*types.TCPRetransmitTracing](tcpSharkToolName, handleTCPRetransmitEvent)
}

func newTCPRetransmit() (*tracing.EventTracingAttr, error) {
	if err := validateTCPRetransmitFilter(configSnapshot()); err != nil {
		return nil, err
	}
	return &tracing.EventTracingAttr{
		TracingData: &tcpRetransmitTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func validateTCPRetransmitFilter(config *Config) error {
	if !config.TCPRetransmit.EnableDropwatchCorrelation {
		return nil
	}
	if err := pcapfilter.ValidateL3Compatible(effectiveTCPRetransmitFilter(config)); err != nil {
		return fmt.Errorf(
			"EventTracing.TCPRetransmit.Filter is incompatible with local correlation: %w",
			err,
		)
	}
	return nil
}

// Start launches tcpshark in retransmit mode and waits for it to finish.
// Events are received via the default toolstream server registered in init.
func (c *tcpRetransmitTracing) Start(ctx context.Context) error {
	process, err := exec.New(exec.Spec{
		Path: path.Join(internalconfig.CoreBinDir, tcpSharkToolName),
		Args: tcpRetransmitArgs(configSnapshot()),
	})
	if err != nil {
		return fmt.Errorf("create %s process: %w", tcpSharkToolName, err)
	}
	if err := process.Run(ctx); err != nil {
		if errors.Is(err, exec.ErrStopFailed) {
			stopErr := process.Stop(ctx)
			if stopErr == nil && errors.Is(err, context.Canceled) {
				return nil
			}
			if stopErr != nil {
				err = errors.Join(
					err,
					fmt.Errorf("retry stop %s: %w", tcpSharkToolName, stopErr),
				)
			}
		} else if errors.Is(err, context.Canceled) {
			return nil
		}
		if stderr := process.Stderr(); len(stderr) > 0 {
			return fmt.Errorf("run %s: %w; stderr: %s", tcpSharkToolName, err, stderr)
		}
		return fmt.Errorf("run %s: %w", tcpSharkToolName, err)
	}
	return nil
}

func tcpRetransmitArgs(config *Config) []string {
	args := []string{
		"--mode", "retransmit",
		"--output-storage", toolstream.DefaultSockPath,
		"--max-events-per-second", strconv.FormatUint(config.TCPRetransmit.MaxEventsPerSecond, 10),
		"--source-types", toolstream.SourceTypeEvent,
	}
	if config.TCPRetransmit.EnableDropwatchCorrelation {
		args = append(
			args,
			"--with-dropwatch",
			"--bpf-path-dir", internalconfig.CoreBpfDir,
		)
	} else {
		args = append(
			args,
			"--bpf-path", path.Join(internalconfig.CoreBpfDir, "tcp_retransmit.o"),
		)
	}
	if filter := effectiveTCPRetransmitFilter(config); filter != "" {
		args = append(args, "--filter", filter)
	}
	if config.TCPRetransmit.EnableTLP {
		args = append(args, "--enable-tlp")
	}
	return args
}

func handleTCPRetransmitEvent(_ *toolstream.Session, ev *types.TCPRetransmitTracing) error {
	if ev.ContainerID == "" {
		ev.ContainerID = pod.ContainerIDByCgroupNetNamespace(pod.ContainerCgroupNetNamespace{
			MemoryCgroupCSSAddr: kernaddr.ParseOrZero(ev.MemoryCgroupCSSAddr),
			NetNamespaceCookie:  ev.NetNamespaceCookie,
			NetNamespaceInum:    uint64(ev.NetNamespaceInum),
		})
	}

	observedTimestamp, err := timeutil.Parse(ev.ObservedTimestamp)
	if err != nil {
		return fmt.Errorf("parse tcp retransmit observed timestamp: %w", err)
	}
	tracerData := *ev
	tracerData.ObservedTimestamp = ""
	return tracing.Save(&tracing.WriteRequest{
		TracerName:        tcpRetransmitTracerName,
		ContainerID:       ev.ContainerID,
		ObservedTimestamp: observedTimestamp,
		TracerData:        &tracerData,
	})
}
