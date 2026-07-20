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
	"fmt"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/net_tx_latency.c -o $BPF_DIR/net_tx_latency.o

type netTxLatTracing struct{}

// The TX BPF perf_event_t is intentionally identical in layout to the RX one,
// so it reuses NetTracingData for storage/Grafana consistency. The lat_stage
// field carries a TX_STAGE_* value from txStageNames.
type netTxPerfEvent = netRcvPerfEvent

var txStageNames = []string{
	"TX_STAGE_SENDMSG", // tcp_sendmsg -> net_dev_queue
	"TX_STAGE_NIC",     // net_dev_queue -> net_dev_xmit
}

func init() {
	tracing.RegisterEventTracing("net_tx_latency", newNetTxLat)
}

func newNetTxLat() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netTxLatTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (c *netTxLatTracing) Start(ctx context.Context) error {
	txlatThreshSendmsg := cfg.NetTxLatency.Sendmsg2Qdisc // ms, tcp_sendmsg -> qdisc (net_dev_queue)
	txlatThreshNic := cfg.NetTxLatency.Qdisc2Nic         // ms, qdisc -> nic (net_dev_xmit)

	if txlatThreshSendmsg == 0 || txlatThreshNic == 0 {
		return fmt.Errorf("net_tx_latency threshold [%v %v]ms invalid", txlatThreshSendmsg, txlatThreshNic)
	}

	log.Debugf("net_tx_latency start, latency threshold [%v %v]ms", txlatThreshSendmsg, txlatThreshNic)

	latThresholds := []uint64{txlatThreshSendmsg, txlatThreshNic}

	// TX timestamps are all bpf_ktime_get_ns (monotonic) across tcp_sendmsg,
	// net_dev_queue and net_dev_xmit, so no mono/wall offset is needed (unlike
	// RX, which diffs against the skb's receive timestamp).

	args := map[string]any{
		"txlat_thresh_sendmsg": txlatThreshSendmsg * 1000 * 1000,
		"txlat_thresh_nic":     txlatThreshNic * 1000 * 1000,
	}
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), args)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "net_tx_lat_event_map", 8192)
	if err != nil {
		return err
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	// save host netns
	hostNetNsInode, err := netutil.NetNSInodeByPid(1)
	if err != nil {
		return fmt.Errorf("get host netns inode: %w", err)
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var pd netTxPerfEvent
			if err := reader.ReadInto(&pd); err != nil {
				return fmt.Errorf("read from perf event fail: %w", err)
			}

			containerID, ok := filterTxByConfigAndResolveContainerID(&pd, hostNetNsInode)
			if !ok {
				continue
			}

			where, latThreshold, stageOK := lookupLatStage(pd.LatStage, txStageNames, latThresholds)
			if !stageOK {
				log.Warnf("net_tx_latency: unknown lat_stage %d, skipping", pd.LatStage)
				continue
			}
			lat := float64(pd.Latency) / 1000 / 1000 // ms
			state := packet.TCPStateName(pd.TCPState)
			addrFamily, saddr, daddr := pd.addrs()
			sport, dport := netutil.Ntohs(pd.TCPSport), netutil.Ntohs(pd.TCPDport)
			seq, ackSeq := netutil.Ntohl(pd.TCPSeq), netutil.Ntohl(pd.TCPAckSeq)
			pktLen := pd.PktLen

			comm := bytesutil.ToStr(pd.Comm[:])
			pid := pd.TgidPid >> 32

			title := fmt.Sprintf("comm=%s:%d to=%s lat(ms)=%.2f state=%s saddr=%s sport=%d daddr=%s dport=%d seq=%d ackSeq=%d pktLen=%d",
				comm, pid, where, lat, state, saddr, sport, daddr, dport, seq, ackSeq, pktLen)

			// known issue filter
			if _, found := matcher.Classify(cfg.IssuesList, title); found {
				log.Debugf("net_tx_latency known issue")
				continue
			}

			tracerData := &NetTracingData{
				Comm:               comm,
				Pid:                pid,
				LatStage:           where,
				LatMs:              lat,
				LatThresholds:      latThreshold,
				NetdevName:         bytesutil.ToStr(pd.NetdevName[:]),
				NetNamespaceInode:  pd.NetNamespaceInode,
				NetNamespaceCookie: pd.NetNamespaceCookie,
				TCPState:           state,
				AddressFamily:      addrFamily,
				TCPSaddr:           saddr,
				TCPDaddr:           daddr,
				TCPSport:           sport,
				TCPDport:           dport,
				TCPSeq:             seq,
				TCPAckSeq:          ackSeq,
				PktLen:             pktLen,
			}
			log.Debugf("net_tx_latency tracerData: %+v", tracerData)

			// save storage
			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:  "net_tx_latency",
				ContainerID: containerID,
				TracerTime:  time.Now(),
				TracerData:  tracerData,
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

func isTxQosExcluded(container *pod.Container) bool {
	for _, level := range cfg.NetTxLatency.ExcludedContainerQos {
		if strings.EqualFold(container.Qos.String(), level) {
			return true
		}
	}
	return false
}

func filterTxByConfigAndResolveContainerID(pd *netTxPerfEvent, hostNetnsInode uint64) (string, bool) {
	inode := uint64(pd.NetNamespaceInode)

	if cfg.NetTxLatency.ExcludedHostNetnamespace && inode == hostNetnsInode {
		return "", false
	}

	var container *pod.Container

	if pd.NetNamespaceCookie != 0 {
		ct, err := pod.ContainerByNetCookie(pd.NetNamespaceCookie)
		if err != nil {
			log.Debugf("net_tx_latency: net_cookie lookup %d failed: %v", pd.NetNamespaceCookie, err)
		} else if ct != nil {
			container = ct
		}
	}

	if container == nil {
		ct, err := pod.ContainerByNetInode(inode)
		if err != nil {
			log.Warnf("net_tx_latency: get container by netns inode %d failed: %v", inode, err)
			return "", true
		}
		if ct == nil {
			return "", true
		}
		container = ct
	}

	if isTxQosExcluded(container) {
		return container.ID, false
	}
	return container.ID, true
}
