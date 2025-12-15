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
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/internal/utils/procfsutil"
	"huatuo-bamai/pkg/tracing"

	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/net_rx_latency.c -o $BPF_DIR/net_rx_latency.o

type netRecvLatTracing struct{}

// NetTracingData is the full data structure.
type NetTracingData struct {
	Comm    string `json:"comm"`
	Pid     uint64 `json:"pid"`
	Where   string `json:"where"`
	Latency uint64 `json:"latency_ms"`
	State   string `json:"state"`
	Saddr   string `json:"saddr"`
	Daddr   string `json:"daddr"`
	Sport   uint16 `json:"sport"`
	Dport   uint16 `json:"dport"`
	Seq     uint32 `json:"seq"`
	AckSeq  uint32 `json:"ack_seq"`
	PktLen  uint64 `json:"pkt_len"`
}

// from bpf perf
type netRcvPerfEvent struct {
	Comm    [bpf.TaskCommLen]byte
	Latency uint64
	TgidPid uint64
	PktLen  uint64
	Sport   uint16
	Dport   uint16
	Saddr   uint32
	Daddr   uint32
	Seq     uint32
	AckSeq  uint32
	State   uint8
	Where   uint8
}

// from include/net/tcp_states.h
var tcpStateMap = []string{
	"<nil>", // 0
	"ESTABLISHED",
	"SYN_SENT",
	"SYN_RECV",
	"FIN_WAIT1",
	"FIN_WAIT2",
	"TIME_WAIT",
	"CLOSE",
	"CLOSE_WAIT",
	"LAST_ACK",
	"LISTEN",
	"CLOSING",
	"NEW_SYN_RECV",
}

const userCopyCase = 2

var toWhere = []string{
	"TO_NETIF_RCV",
	"TO_TCPV4_RCV",
	"TO_USER_COPY",
}

func init() {
	tracing.RegisterEventTracing("net_rx_latency", newNetRcvLat)
}

func newNetRcvLat() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netRecvLatTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (c *netRecvLatTracing) Start(ctx context.Context) error {
	toNetIf := conf.Get().EventTracing.NetRxLatency.Driver2NetRx        // ms, before RPS to a core recv(__netif_receive_skb)
	toTCPV4 := conf.Get().EventTracing.NetRxLatency.Driver2TCP          // ms, before RPS to TCP recv(tcp_v4_rcv)
	toUserCopy := conf.Get().EventTracing.NetRxLatency.Driver2Userspace // ms, before RPS to user recv(skb_copy_datagram_iovec)

	if toNetIf == 0 || toTCPV4 == 0 || toUserCopy == 0 {
		return fmt.Errorf("net_rx_latency threshold [%v %v %v]ms invalid", toNetIf, toTCPV4, toUserCopy)
	}
	log.Infof("net_rx_latency start, latency threshold [%v %v %v]ms", toNetIf, toTCPV4, toUserCopy)

	monoWallOffset, err := estMonoWallOffset()
	if err != nil {
		return fmt.Errorf("estimate monoWallOffset failed: %w", err)
	}

	log.Infof("net_rx_latency offset of mono to walltime: %v ns", monoWallOffset)

	args := map[string]any{
		"mono_wall_offset": monoWallOffset,
		"to_netif":         toNetIf * 1000 * 1000,
		"to_tcpv4":         toTCPV4 * 1000 * 1000,
		"to_user_copy":     toUserCopy * 1000 * 1000,
	}
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), args)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "net_recv_lat_event_map", 8192)
	if err != nil {
		return err
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	// save host netns
	hostNetNsInode, err := procfsutil.NetNSInodeByPid(1)
	if err != nil {
		return fmt.Errorf("get host netns inode: %w", err)
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var pd netRcvPerfEvent
			if err := reader.ReadInto(&pd); err != nil {
				return fmt.Errorf("read rrom perf event fail: %w", err)
			}
			tracerTime := time.Now()

			comm := "<nil>" // not in process context
			var pid uint64
			var containerID string
			if pd.TgidPid != 0 {
				comm = bytesutil.ToString(pd.Comm[:])
				pid = pd.TgidPid >> 32

				// check if its netns same as host netns
				if pd.Where == userCopyCase {
					cid, skip, err := ignore(pid, comm, hostNetNsInode)
					if err != nil {
						return err
					}
					if skip {
						continue
					}
					containerID = cid
				}
			}

			where := toWhere[pd.Where]
			lat := pd.Latency / 1000 / 1000 // ms
			state := tcpStateMap[pd.State]
			saddr, daddr := netutil.InetNtop(pd.Saddr).String(), netutil.InetNtop(pd.Daddr).String()
			sport, dport := netutil.InetNtohs(pd.Sport), netutil.InetNtohs(pd.Dport)
			seq, ackSeq := netutil.InetNtohl(pd.Seq), netutil.InetNtohl(pd.AckSeq)
			pktLen := pd.PktLen

			title := fmt.Sprintf("comm=%s:%d to=%s lat(ms)=%v state=%s saddr=%s sport=%d daddr=%s dport=%d seq=%d ackSeq=%d pktLen=%d",
				comm, pid, where, lat, state, saddr, sport, daddr, dport, seq, ackSeq, pktLen)

			// tcp state filter
			if (state != "ESTABLISHED") && (state != "<nil>") {
				continue
			}

			// known issue filter
			caseName, _ := conf.KnownIssueSearch(title, "", "")
			if caseName == "net_rx_latency" {
				log.Debugf("net_rx_latency known issue")
				continue
			}

			tracerData := &NetTracingData{
				Comm:    comm,
				Pid:     pid,
				Where:   where,
				Latency: lat,
				State:   state,
				Saddr:   saddr,
				Daddr:   daddr,
				Sport:   sport,
				Dport:   dport,
				Seq:     seq,
				AckSeq:  ackSeq,
				PktLen:  pktLen,
			}
			log.Debugf("net_rx_latency tracerData: %+v", tracerData)

			// save storage
			storage.Save("net_rx_latency", containerID, tracerTime, tracerData)
		}
	}
}

func ignore(pid uint64, comm string, hostNetnsInode uint64) (containerID string, skip bool, err error) {
	// check if its netns same as host netns
	dstInode, err := procfsutil.NetNSInodeByPid(int(pid))
	if err != nil {
		// ignore the missing program
		if errors.Is(err, syscall.ENOENT) {
			return "", true, nil
		}
		return "", skip, fmt.Errorf("get netns inode of pid %v failed: %w", pid, err)
	}
	if conf.Get().EventTracing.NetRxLatency.ExcludedHostNetnamespace && dstInode == hostNetnsInode {
		log.Debugf("ignore %s:%v the same netns as host", comm, pid)
		return "", true, nil
	}

	// check container level
	var container *pod.Container
	if container, err = pod.ContainerByNetNamespaceInode(dstInode); err != nil {
		log.Warnf("get container info by netns inode %v pid %v, failed: %v", dstInode, pid, err)
	}
	if container != nil {
		for _, level := range conf.Get().EventTracing.NetRxLatency.ExcludedContainerQos {
			if strings.EqualFold(container.Qos.String(), level) {
				log.Debugf("ignore container %+v", container)
				skip = true
				break
			}
		}
		containerID = container.ID
	}

	return containerID, skip, nil
}

// estimate the offset between clock monotonic and real time
// bpf_ktime_get_ns() access to clock monotonic, but skb->tstamp = ktime_get_real() at netif_receive_skb_internal
// ref: https://github.com/torvalds/linux/blob/v4.18/net/core/dev.c#L4736
// t3 - t2 + (t3 - t1) / 2 => (t3 + t1) / 2 - t2
func estMonoWallOffset() (int64, error) {
	var t1, t2, t3 unix.Timespec
	var bestDelta int64
	var offset int64

	for i := 0; i < 10; i++ {
		err1 := unix.ClockGettime(unix.CLOCK_REALTIME, &t1)
		err2 := unix.ClockGettime(unix.CLOCK_MONOTONIC, &t2)
		err3 := unix.ClockGettime(unix.CLOCK_REALTIME, &t3)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, fmt.Errorf("%w, %w, %w", err1, err2, err3)
		}

		delta := unix.TimespecToNsec(t3) - unix.TimespecToNsec(t1)
		if i == 0 || delta < bestDelta {
			bestDelta = delta
			offset = (unix.TimespecToNsec(t3)+unix.TimespecToNsec(t1))/2 - unix.TimespecToNsec(t2)
		}
	}

	return offset, nil
}
