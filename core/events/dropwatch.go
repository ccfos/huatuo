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

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
	"github.com/ccfos/huatuo/pkg/types"
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

	process, err := exec.New(exec.Spec{
		Path: path.Join(internalconfig.CoreBinDir, "dropwatch"),
		Args: args,
	})
	if err != nil {
		return fmt.Errorf("create dropwatch process: %w", err)
	}
	if err := process.Run(ctx); err != nil {
		if errors.Is(err, exec.ErrStopFailed) {
			stopErr := process.Stop(ctx)
			if stopErr == nil && errors.Is(err, context.Canceled) {
				return nil
			}
			if stopErr != nil {
				err = errors.Join(err, fmt.Errorf("retry stop dropwatch: %w", stopErr))
			}
		} else if errors.Is(err, context.Canceled) {
			return nil
		}
		if stderr := process.Stderr(); len(stderr) > 0 {
			return fmt.Errorf("run dropwatch: %w; stderr: %s", err, stderr)
		}
		return fmt.Errorf("run dropwatch: %w", err)
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
	observedTimestamp, err := timeutil.Parse(ev.ObservedTimestamp)
	if err != nil {
		return fmt.Errorf("parse dropwatch observed timestamp: %w", err)
	}
	tracerData := *ev
	tracerData.ObservedTimestamp = ""

	return tracing.Save(&tracing.WriteRequest{
		TracerName:        "dropwatch",
		ContainerID:       ev.ContainerID,
		ObservedTimestamp: observedTimestamp,
		TracerData:        &tracerData,
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
