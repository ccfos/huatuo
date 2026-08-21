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
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
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
	return &tracing.EventTracingAttr{
		TracingData: &tcpRetransmitTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

// Start launches tcpshark in retransmit mode and waits for it to finish.
// Events are received via the default toolstream server registered in init.
func (c *tcpRetransmitTracing) Start(ctx context.Context) error {
	globalDropwatchTCPRetransmitCache.enable()
	defer globalDropwatchTCPRetransmitCache.disable()

	cfg := configSnapshot()
	args := []string{
		"--mode", "retransmit",
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "tcp_retransmit.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--max-events-per-second", strconv.FormatUint(cfg.TCPRetransmit.MaxEventsPerSecond, 10),
		"--source-types", toolstream.SourceTypeEvent,
	}

	if cfg.TCPRetransmit.Filter != "" {
		args = append(args, "--filter", cfg.TCPRetransmit.Filter)
	}
	if cfg.TCPRetransmit.EnableTLP {
		args = append(args, "--enable-tlp")
	}

	result := executil.ExecCmd(ctx, 0, path.Join(internalconfig.CoreBinDir, tcpSharkToolName), args...)
	if errors.Is(result.CmdErr, context.Canceled) {
		return nil
	}
	if result.CmdErr != nil {
		return fmt.Errorf("run %s: %w", tcpSharkToolName, executil.VerifyResults([]executil.CmdResult{result}))
	}
	return nil
}

func handleTCPRetransmitEvent(_ *toolstream.Session, ev *types.TCPRetransmitTracing) error {
	if ev.ContainerID == "" {
		ev.ContainerID = pod.ContainerIDByCgroupNetNamespace(pod.ContainerCgroupNetNamespace{
			MemoryCgroupCSSAddr: kernaddr.ParseOrZero(ev.MemoryCgroupCSSAddr),
			NetNamespaceCookie:  ev.NetNamespaceCookie,
			NetNamespaceInum:    uint64(ev.NetNamespaceInum),
		})
	}

	if ev.DropLocation == "" {
		causal, _ := globalDropwatchTCPRetransmitCache.correlate(ev)
		ev.DropLocation = causalToDropLocation(causal)
	}

	return tracing.Save(&tracing.WriteRequest{
		TracerName:  tcpRetransmitTracerName,
		ContainerID: ev.ContainerID,
		TracerTime:  time.Now(),
		TracerData:  ev,
	})
}
