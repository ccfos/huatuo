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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/net_retransmit_skb.c -o $BPF_DIR/net_retransmit_skb.o

type netRetransmitTracing struct{}

// NetRetransmitTracingData is the canonical JSON schema for a TCP retransmit
// event, used to build the packet-loss / retransmit network topology (#325).
type NetRetransmitTracingData struct {
	ObservedTimestamp   string `json:"observed_timestamp"`
	Comm                string `json:"comm"`
	Pid                 uint64 `json:"pid"`
	ContainerID         string `json:"container_id,omitempty"`
	MemoryCgroupCSSAddr string `json:"memory_cgroup_css_addr,omitempty"`
	NetNamespaceCookie  uint64 `json:"net_namespace_cookie"`
	NetNamespaceInode   uint32 `json:"net_namespace_inode"`
	NetdevName          string `json:"netdev_name"`
	TCPState            string `json:"tcp_state"`
	AddressFamily       string `json:"address_family"`
	TCPSaddr            string `json:"tcp_saddr"`
	TCPDaddr            string `json:"tcp_daddr"`
	TCPSport            uint16 `json:"tcp_sport"`
	TCPDport            uint16 `json:"tcp_dport"`
	TCPSeq              uint32 `json:"tcp_seq"`
	PktLen              uint64 `json:"pkt_len"`
	KtimeNS             uint64 `json:"ktime_ns"`
}

// netRetransmitPerfEvent mirrors bpf/net_retransmit_skb.c perf_event_t
// byte-for-byte (120 bytes). Pinned with the size assert below so a stale
// field cannot silently corrupt the decode.
type netRetransmitPerfEvent struct {
	Ktime_ns     uint64
	TgidPid      uint64
	MemcgCSSAddr uint64
	NetCookie    uint64
	PktLen       uint64
	NetnsInum    uint32
	TCPSeq       uint32
	TCPSport     uint16
	TCPDport     uint16
	AddrFamily   uint8
	TCPState     uint8
	_            [2]byte
	TCPSaddr     [16]byte
	TCPDaddr     [16]byte
	Comm         [bpf.TaskCommLen]byte
	NetdevName   [bpf.NetdevNameLen]byte
}

var _ = [1]struct{}{}[120-unsafe.Sizeof(netRetransmitPerfEvent{})]

func init() {
	tracing.RegisterEventTracing("net_retransmit", newNetRetransmit)
}

func newNetRetransmit() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netRetransmitTracing{},
		Flag:        tracing.FlagTracing,
	}, nil
}

func (c *netRetransmitTracing) Start(ctx context.Context) error {
	// tcp_retransmit_skb is core TCP; gate anyway so a kernel without the
	// symbol degrades to inactive instead of failing the whole tracer.
	if !bpf.HasKprobeFunction("tcp_retransmit_skb") {
		return types.ErrNotSupported
	}

	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.EventPipeByName(childCtx, "net_retrans_event_map", 8192)
	if err != nil {
		return err
	}

	if err := b.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "tcp_retransmit_skb_prog", Symbol: "tcp_retransmit_skb"},
	}); err != nil {
		return errors.Join(err, reader.Close())
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
		}

		var pd netRetransmitPerfEvent
		if err := reader.ReadInto(&pd); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("read from perf event fail: %w", err)
		}

		containerID := resolveRetransmitContainer(&pd)

		family, saddr, daddr := pd.addrs()
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName:  "net_retransmit",
			ContainerID: containerID,
			TracerTime:  time.Now(),
			TracerData: &NetRetransmitTracingData{
				ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
				Comm:                bytesutil.ToStr(pd.Comm[:]),
				Pid:                 pd.TgidPid >> 32,
				ContainerID:         containerID,
				MemoryCgroupCSSAddr: kernaddr.Format(pd.MemcgCSSAddr),
				NetNamespaceCookie:  pd.NetCookie,
				NetNamespaceInode:   pd.NetnsInum,
				NetdevName:          bytesutil.ToStr(pd.NetdevName[:]),
				TCPState:            packet.TCPStateName(pd.TCPState),
				AddressFamily:       family,
				TCPSaddr:            saddr,
				TCPDaddr:            daddr,
				TCPSport:            netutil.Ntohs(pd.TCPSport),
				TCPDport:            netutil.Ntohs(pd.TCPDport),
				TCPSeq:              netutil.Ntohl(pd.TCPSeq),
				PktLen:              pd.PktLen,
				KtimeNS:             pd.Ktime_ns,
			},
		}); err != nil {
			log.Warnf("net_retransmit: save tracing data: %v", err)
		}
	}
}

// resolveRetransmitContainer attributes a retransmit to a container, trying the
// most specific identifier first (memory cgroup CSS), then net namespace cookie
// (>= 5.14), then net namespace inode (always available). Returns "" for
// host-network traffic — left un-attributed, matching dropwatch/net_rx.
func resolveRetransmitContainer(pd *netRetransmitPerfEvent) string {
	if pd.MemcgCSSAddr != 0 {
		if ct, err := pod.ContainerByCSS(pd.MemcgCSSAddr, subsystem.SubsystemMemory); err != nil {
			log.Debugf("net_retransmit: CSS lookup %d: %v", pd.MemcgCSSAddr, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	if pd.NetCookie != 0 {
		if ct, err := pod.ContainerByNetCookie(pd.NetCookie); err != nil {
			log.Debugf("net_retransmit: net_cookie lookup %d: %v", pd.NetCookie, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	if pd.NetnsInum != 0 {
		if ct, err := pod.ContainerByNetInode(uint64(pd.NetnsInum)); err != nil {
			log.Debugf("net_retransmit: net_inum lookup %d: %v", pd.NetnsInum, err)
		} else if ct != nil {
			return ct.ID
		}
	}

	return ""
}

func (e *netRetransmitPerfEvent) addrs() (family, saddr, daddr string) {
	// AF_INET6 = 10, AF_INET = 2 (bpf/include/vmlinux_net.h). Inlined as
	// literals so this file does not depend on net_rx_latency.go's afINET*
	// consts and stays self-contained on main.
	if e.AddrFamily == 10 { // AF_INET6
		return "ipv6",
			net.IP(e.TCPSaddr[:]).String(),
			net.IP(e.TCPDaddr[:]).String()
	}
	return "ipv4",
		net.IPv4(e.TCPSaddr[0], e.TCPSaddr[1], e.TCPSaddr[2], e.TCPSaddr[3]).String(),
		net.IPv4(e.TCPDaddr[0], e.TCPDaddr[1], e.TCPDaddr[2], e.TCPDaddr[3]).String()
}

// readNetRetransmitPerfEvent decodes a raw perf record into
// netRetransmitPerfEvent. Exposed for tests so the byte-layout mirror can be
// verified without a live BPF program.
func readNetRetransmitPerfEvent(r io.Reader) (*netRetransmitPerfEvent, error) {
	var pd netRetransmitPerfEvent
	if err := binary.Read(r, binary.LittleEndian, &pd); err != nil {
		return nil, err
	}
	return &pd, nil
}
