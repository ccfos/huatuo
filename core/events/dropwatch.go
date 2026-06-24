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
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
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
	args := []string{
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "dropwatch.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--filter", cfg.Dropwatch.Filter,
		"--max-events-per-second", strconv.FormatUint(cfg.Dropwatch.MaxEventsPerSecond, 10),
	}

	cmd := exec.Command(path.Join(internalconfig.CoreBinDir, "dropwatch"), args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start dropwatch: %w", err)
	}

	log.Infof("dropwatch started pid=%d", cmd.Process.Pid)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		log.Info("dropwatch stopped")
		return nil
	case werr := <-done:
		if werr != nil {
			return fmt.Errorf("dropwatch exited: %w", werr)
		}
		log.Info("dropwatch exited")
		return nil
	}
}

func handleDropwatchEvent(_ *toolstream.Session, ev *types.DropWatchTracing) error {
	if ignoreDropwatch(ev) {
		return nil
	}

	if ev.ContainerID == "" {
		ev.ContainerID = resolveContainerIDFromMeta(ev)
	}

	return tracing.Save(&tracing.WriteRequest{
		TracerName:  "dropwatch",
		ContainerID: ev.ContainerID,
		TracerTime:  time.Now(),
		TracerData:  ev,
	})
}

func resolveContainerIDFromMeta(ev *types.DropWatchTracing) string {
	// 1. memcg CSS address — uniquely identifies a container.
	if addr, ok := kernaddr.Parse(ev.MemoryCgroupCSSAddr); ok {
		ct, err := pod.ContainerByCSS(addr, pod.SubSysMemory)
		if err != nil {
			log.Debugf("dropwatch: CSS lookup %s: %v", ev.MemoryCgroupCSSAddr, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	// 2. net namespace cookie — unique per netns; not available on kernels < 5.14.
	// Returns one container sharing the namespace.
	if ev.NetNamespaceCookie != 0 {
		ct, err := pod.ContainerByNetCookie(ev.NetNamespaceCookie)
		if err != nil {
			log.Debugf("dropwatch: net_cookie lookup %d: %v", ev.NetNamespaceCookie, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	// 3. net namespace inode — always available, returns one container sharing the namespace.
	if ev.NetNamespaceInode != 0 {
		ct, err := pod.ContainerByNetInode(uint64(ev.NetNamespaceInode))
		if err != nil {
			log.Debugf("dropwatch: net_inum lookup %d: %v", ev.NetNamespaceInode, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	return ""
}

// ignoreDropwatch returns true for known-noisy events that should not be forwarded.
// Stack frame matching uses the same patterns as the previous TCP-only tracer.
func ignoreDropwatch(data *types.DropWatchTracing) bool {
	stack := strings.Split(data.Stack, "\n")

	// state: CLOSE_WAIT
	// stack:
	// 1. kfree_skb/ffffffff963047b0
	// 2. kfree_skb/ffffffff963047b0
	// 3. skb_rbtree_purge/ffffffff963089e0
	// 4. tcp_fin/ffffffff963ac200
	// 5. ...
	// CLOSE_WAIT + skb_rbtree_purge: normal socket teardown, not a drop.
	if data.Layers != nil && data.Layers.TCP != nil && data.Layers.TCP.SkState == "CLOSE_WAIT" {
		if len(stack) >= 3 && strings.HasPrefix(stack[2], "skb_rbtree_purge/") {
			return true
		}
	}

	// Operator-configured stack-frame noise rules (e.g. bnxt_tx_int,
	// neigh_invalidate). Patterns live in events.IssuesList; see
	// net_rx_latency.go for the same pattern. Match against data.Stack
	// (frames joined by '\n').
	if cfg != nil {
		if _, found := matcher.Classify(cfg.IssuesList, data.Stack); found {
			return true
		}
	}

	return false
}
