// Copyright 2025 The HuaTuo Authors
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
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	internalconfig "huatuo-bamai/internal/config"
)

type dropWatchTracing struct{}

func init() {
	tracing.RegisterEventTracing("dropwatch", newDropWatch)
}

func newDropWatch() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &dropWatchTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

// Start launches dropwatch as a subprocess, receives its events via toolstream,
// filters them, and persists each event with tracing.Save.
func (c *dropWatchTracing) Start(ctx context.Context) error {
	sockPath := path.Join(os.TempDir(), fmt.Sprintf("dropwatch-%d.sock", os.Getpid()))
	_ = os.Remove(sockPath)

	srv, err := toolstream.NewServer(sockPath)
	if err != nil {
		return fmt.Errorf("dropwatch: toolstream server: %w", err)
	}

	defer srv.Close()

	toolstream.Register(srv, "dropwatch", c.handleEvent)

	if err := srv.Start(); err != nil {
		return fmt.Errorf("dropwatch: toolstream start: %w", err)
	}

	args := []string{
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "dropwatch.o"),
		"--output-storage", sockPath,
	}
	if cfg != nil && cfg.Dropwatch.Filter != "" {
		args = append(args, "--filter", cfg.Dropwatch.Filter)
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

func (c *dropWatchTracing) handleEvent(_ *toolstream.Session, ev *types.DropWatchTracing) error {
	if c.ignore(ev) {
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
	if ev.MemcgCssAddr != 0 {
		ct, err := pod.ContainerByCSS(ev.MemcgCssAddr, pod.SubSysMemory)
		if err != nil {
			log.Debugf("dropwatch: CSS lookup 0x%x: %v", ev.MemcgCssAddr, err)
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

// ignore returns true for known-noisy events that should not be forwarded.
// Stack frame matching uses the same patterns as the previous TCP-only tracer.
func (c *dropWatchTracing) ignore(data *types.DropWatchTracing) bool {
	stack := strings.Split(data.Stack, "\n")

	// state: CLOSE_WAIT
	// stack:
	// 1. kfree_skb/ffffffff963047b0
	// 2. kfree_skb/ffffffff963047b0
	// 3. skb_rbtree_purge/ffffffff963089e0
	// 4. tcp_fin/ffffffff963ac200
	// 5. ...
	// CLOSE_WAIT + skb_rbtree_purge: normal socket teardown, not a drop.
	if skState := packet.TCPSkState(data.PacketInfo); skState == "CLOSE_WAIT" {
		if len(stack) >= 3 && strings.HasPrefix(stack[2], "skb_rbtree_purge/") {
			return true
		}
	}

	// stack:
	// 1. kfree_skb/ffffffff96d127b0
	// 2. kfree_skb/ffffffff96d127b0
	// 3. neigh_invalidate/ffffffff96d388b0
	// 4. neigh_timer_handler/ffffffff96d3a870
	// 5. ...
	// neigh_invalidate: ARP/neighbor table cleanup, filtered by config.
	if len(stack) >= 3 && strings.HasPrefix(stack[2], "neigh_invalidate/") {
		return true
	}

	// stack:
	// 1. kfree_skb/ffffffff82283d10
	// 2. kfree_skb/ffffffff82283d10
	// 3. bnxt_tx_int/ffffffffc05c6f20
	// 4. __bnxt_poll_work_done/ffffffffc05c50c0
	// 5. ...
	//
	// stack:
	// 1. kfree_skb/ffffffffaba83d10
	// 2. kfree_skb/ffffffffaba83d10
	// 3. __bnxt_tx_int/ffffffffc045df90
	// 4. bnxt_tx_int/ffffffffc045e250
	// 5. ...
	// bnxt NIC TX completion path: driver frees skb normally, not a real drop.
	if len(stack) >= 3 &&
		(strings.HasPrefix(stack[2], "bnxt_tx_int/") ||
			strings.HasPrefix(stack[2], "__bnxt_tx_int/")) {
		return true
	}

	return false
}
