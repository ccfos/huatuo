// Copyright 2025, 2026 The HuaTuo Authors
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
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

type dropWatchTracing struct{}

func init() {
	tracing.RegisterEventTracing("dropwatch", newDropWatch)
	toolstream.RegisterDefault[*types.DropWatchTracing]("dropwatch", handleDropwatchEvent)
}

func newDropWatch() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &dropWatchTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

// Start launches dropwatch as a subprocess and waits for it to finish.
// Events are received via the default toolstream server registered in init.
func (c *dropWatchTracing) Start(ctx context.Context) error {
	cfg := configSnapshot()
	args := []string{
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "dropwatch.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--filter", cfg.Dropwatch.Filter,
		"--max-events-per-second", strconv.FormatUint(cfg.Dropwatch.MaxEventsPerSecond, 10),
		"--source-types", toolstream.SourceTypeEvent,
	}

	result := executil.ExecCmd(ctx, 0, path.Join(internalconfig.CoreBinDir, "dropwatch"), args...)
	if errors.Is(result.CmdErr, context.Canceled) {
		return nil
	}
	if result.CmdErr != nil {
		return fmt.Errorf("run dropwatch: %w", executil.VerifyResults([]executil.CmdResult{result}))
	}
	return nil
}

func handleDropwatchEvent(_ *toolstream.Session, ev *types.DropWatchTracing) error {
	if ignoreDropwatch(ev) {
		return nil
	}

	if ev.ContainerID == "" {
		ev.ContainerID = pod.ContainerIDByCgroupNetNamespace(pod.ContainerCgroupNetNamespace{
			MemoryCgroupCSSAddr: kernaddr.ParseOrZero(ev.MemoryCgroupCSSAddr),
			NetNamespaceCookie:  ev.NetNamespaceCookie,
			NetNamespaceInum:    uint64(ev.NetNamespaceInum),
		})
	}

	globalDropwatchTCPRetransmitCache.add(ev)

	return tracing.Save(&tracing.WriteRequest{
		TracerName:  "dropwatch",
		ContainerID: ev.ContainerID,
		TracerTime:  time.Now(),
		TracerData:  ev,
	})
}

// ignoreDropwatch returns true for configured noisy events that should not be forwarded.
func ignoreDropwatch(data *types.DropWatchTracing) bool {
	// Operator-configured stack-frame noise rules (e.g. bnxt_tx_int,
	// neigh_invalidate). Patterns live in events.IssuesList; see
	// net_rx_latency.go for the same pattern. Match against data.Stack
	// (frames joined by '\n').
	cfg := configSnapshot()
	if _, found := matcher.Classify(cfg.IssuesList, data.Stack); found {
		return true
	}

	return false
}
