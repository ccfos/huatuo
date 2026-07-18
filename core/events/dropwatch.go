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

	"huatuo-bamai/internal/cgroups/subsystem"
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

	// Annotate the network-path layer from the drop-location stack frame so
	// kernel drops can be sliced alongside netdev_hw hardware drops. Hardware
	// events arrive with DropLayer already set; never overwrite those.
	if ev.DropLayer == "" {
		ev.DropLayer = classifyDropLayer(ev.Stack)
	}

	return tracing.Save(&tracing.WriteRequest{
		TracerName:  "dropwatch",
		ContainerID: ev.ContainerID,
		TracerTime:  time.Now(),
		TracerData:  ev,
	})
}

// classifyDropLayer maps a kernel dropwatch stack to a network-path layer
// (types.DropLayer*). The first frame is the innermost call — the function
// that decided to free the skb (kfree_skb's location), so it identifies where
// the drop happened.
//
// Hardware drops are out of scope here: they are counted by the NIC/driver
// before an skb reaches the stack and never fire kfree_skb, so netdev_hw emits
// them directly with DropLayerHardware.
//
// The taxonomy is intentionally coarse (protocol / driver / unknown); layering
// it finer (per-L4 protocol, netfilter, socket) is a follow-up. Prefix lists
// are matched against the bare function name, which covers the kallsyms frame
// formats produced by internal/symbol: "fn+offset/size" and "fn/hexaddr"
// (with an optional trailing " [module]").
func classifyDropLayer(stack string) string {
	fn := firstStackFunc(stack)
	if fn == "" {
		return types.DropLayerUnknown
	}

	switch {
	case hasPrefix(fn, protocolFuncPrefixes):
		return types.DropLayerProtocol
	case hasPrefix(fn, driverFuncPrefixes):
		return types.DropLayerDriver
	default:
		return types.DropLayerUnknown
	}
}

// protocolFuncPrefixes covers drops decided inside the L3/L4 stack and the
// socket/filter layer reached after netif_receive_skb.
var protocolFuncPrefixes = []string{
	"ip_", "ipv6_", "ip6_", "inet_",
	"tcp_", "udp_", "icmp", "arp_", "sctp_",
	"sock_queue_rcv", "sk_filter", "skb_copy_datagram", "skb_receive_datagram",
	"nf_hook", "nf_recv", "nf_",
}

// driverFuncPrefixes covers drops at link-layer ingress and queuing before the
// protocol stack: NAPI/GRO receive, netif_receive_skb hand-off, and qdisc.
var driverFuncPrefixes = []string{
	"netif_", "__netif_",
	"napi_", "gro_",
	"sch_", "__dev_queue_xmit", "dev_queue_xmit",
	"netdev_", "enqueue_to_",
}

// firstStackFunc returns the bare function name of the innermost stack frame
// that actually decided the drop. It strips the "+offset/size" or "/hexaddr"
// suffix and any trailing " [module]", and skips the kfree_skb tracepoint
// wrappers that occupy the top of the stack (e.g. "kfree_skb_reason",
// "__kfree_skb") — the real drop location is the first frame after them.
// Empty input or no frame yields "".
func firstStackFunc(stack string) string {
	for _, frame := range strings.Split(stack, "\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		// Strip a trailing module annotation first, e.g. "fn+0x1/0x2 [mlx5_core]".
		if i := strings.LastIndex(frame, " ["); i >= 0 {
			frame = frame[:i]
		}
		// Cut at the first offset/address separator.
		fn := frame
		for i, r := range frame {
			if r == '+' || r == '/' {
				fn = frame[:i]
				break
			}
		}
		if isKfreeSKBWrapper(fn) {
			continue
		}
		return fn
	}
	return ""
}

// isKfreeSKBWrapper reports whether fn is the kfree_skb tracepoint entry point
// itself rather than the function that decided the drop.
func isKfreeSKBWrapper(fn string) bool {
	switch {
	case fn == "kfree_skb", fn == "__kfree_skb", fn == "kfree_skb_reason":
		return true
	case strings.HasPrefix(fn, "trace_kfree_skb"), strings.HasPrefix(fn, "perf_trace_kfree_skb"):
		return true
	}
	return false
}

func hasPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func resolveContainerIDFromMeta(ev *types.DropWatchTracing) string {
	// 1. memcg CSS address — uniquely identifies a container.
	if addr, ok := kernaddr.Parse(ev.MemoryCgroupCSSAddr); ok {
		ct, err := pod.ContainerByCSS(addr, subsystem.SubsystemMemory)
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
