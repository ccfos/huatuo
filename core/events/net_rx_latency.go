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
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/timeutil"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/tracing"

	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/net_rx_latency.c -o $BPF_DIR/net_rx_latency.o

type netRecvLatTracing struct{}

// NetTracingData is the full data structure.
type NetTracingData struct {
	Comm               string  `json:"comm"`
	Pid                uint64  `json:"pid"`
	LatStage           string  `json:"lat_stage"`
	LatMs              float64 `json:"lat_ms"`
	LatThresholds      uint64  `json:"lat_thresholds"`
	NetdevName         string  `json:"netdev_name"`
	NetNamespaceInode  uint32  `json:"net_namespace_inode"`
	NetNamespaceCookie uint64  `json:"net_namespace_cookie"`
	TCPState           string  `json:"tcp_state"`
	AddressFamily      string  `json:"address_family"`
	TCPSaddr           string  `json:"tcp_saddr"`
	TCPDaddr           string  `json:"tcp_daddr"`
	TCPSport           uint16  `json:"tcp_sport"`
	TCPDport           uint16  `json:"tcp_dport"`
	TCPSeq             uint32  `json:"tcp_seq"`
	TCPAckSeq          uint32  `json:"tcp_ack_seq"`
	PktLen             uint64  `json:"pkt_len"`
}

// from bpf perf. Layout mirrors bpf/net_rx_latency.c perf_event_t byte-for-byte;
// TX aliases this same struct (see net_tx_latency.go).
type netRcvPerfEvent struct {
	Comm               [bpf.TaskCommLen]byte
	Latency            uint64
	TgidPid            uint64
	PktLen             uint64
	TCPSport           uint16
	TCPDport           uint16
	AddrFamily         uint8
	_                  [3]byte
	TCPSaddr           [16]byte
	TCPDaddr           [16]byte
	TCPSeq             uint32
	TCPAckSeq          uint32
	TCPState           uint8
	LatStage           uint8
	_                  [2]byte
	NetdevName         [bpf.NetdevNameLen]byte
	NetNamespaceInode  uint32
	NetNamespaceCookie uint64
}

// Address families carried in AddrFamily; mirror bpf/include/vmlinux_net.h.
const (
	afINET  uint8 = 2  // AF_INET
	afINET6 uint8 = 10 // AF_INET6
)

const (
	addrFamilyV4 = "ipv4"
	addrFamilyV6 = "ipv6"
)

// addrs formats the 16-byte src/dst according to AddrFamily. IPv4 occupies the
// low 4 bytes (network order); IPv6 uses all 16. Shared by RX and TX.
func (e *netRcvPerfEvent) addrs() (family, saddr, daddr string) {
	if e.AddrFamily == afINET6 {
		return addrFamilyV6,
			netutil.Inetv6Ntop(e.TCPSaddr).String(),
			netutil.Inetv6Ntop(e.TCPDaddr).String()
	}
	return addrFamilyV4,
		net.IPv4(e.TCPSaddr[0], e.TCPSaddr[1], e.TCPSaddr[2], e.TCPSaddr[3]).String(),
		net.IPv4(e.TCPDaddr[0], e.TCPDaddr[1], e.TCPDaddr[2], e.TCPDaddr[3]).String()
}

var latStageNames = []string{
	"RX_STAGE_NETIF",
	"RX_STAGE_TCPV4",
	"RX_STAGE_USERCOPY",
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
	rxlatThreshNetif := cfg.NetRxLatency.Driver2NetRx        // ms, before RPS to a core recv(__netif_receive_skb)
	rxlatThreshTcpv4 := cfg.NetRxLatency.Driver2TCP          // ms, before RPS to TCP recv(tcp_v4_rcv)
	rxlatThreshUsercopy := cfg.NetRxLatency.Driver2Userspace // ms, before RPS to user recv(skb_copy_datagram_iovec)

	if rxlatThreshNetif == 0 || rxlatThreshTcpv4 == 0 || rxlatThreshUsercopy == 0 {
		return fmt.Errorf("net_rx_latency threshold [%v %v %v]ms invalid", rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy)
	}

	log.Debugf("net_rx_latency start, latency threshold [%v %v %v]ms", rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy)

	latThresholds := []uint64{rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy}

	monoWallOffset, err := timeutil.MonoToRealOffset()
	if err != nil {
		return fmt.Errorf("estimate monoWallOffset failed: %w", err)
	}

	log.Debugf("net_rx_latency offset of mono to walltime: %v ns", monoWallOffset)

	// for tracing 'net_rx_latency' keep the skb timestamp enabled,
	// kernel func net_enable_timestamp() is system wide, can enable by set SOF_TIMESTAMPING_RX_SOFTWARE,
	// ref: https://www.kernel.org/doc/html/latest/networking/timestamping.html.
	tsConn, err := enableSkbTimestamp()
	if err != nil {
		return err
	}
	defer tsConn.Close()

	args := map[string]any{
		"mono_wall_offset":      monoWallOffset,
		"rxlat_thresh_netif":    rxlatThreshNetif * 1000 * 1000,
		"rxlat_thresh_tcpv4":    rxlatThreshTcpv4 * 1000 * 1000,
		"rxlat_thresh_usercopy": rxlatThreshUsercopy * 1000 * 1000,
	}
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), args)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.EventPipeByName(childCtx, "net_recv_lat_event_map", 8192)
	if err != nil {
		return err
	}

	// Attach explicitly (instead of AttachAndEventPipe) so the IPv6 TCP-layer
	// probe can be omitted on kernels without CONFIG_IPV6: Attach() is
	// all-or-nothing, so a missing tcp_v6_rcv must stay out of the list. The
	// netif_receive_skb and skb_copy_datagram_iovec tracepoints are
	// protocol-agnostic, so IPv6 RX latency is still collected (2 of 3 stages)
	// even when tcp_v6_rcv is absent.
	attachOpts := []bpf.AttachOption{
		{ProgramName: "netif_receive_skb_prog", Symbol: "net/netif_receive_skb"},
		{ProgramName: "tcp_v4_rcv_prog", Symbol: "tcp_v4_rcv"},
		{ProgramName: "skb_copy_datagram_iovec_prog", Symbol: "skb/skb_copy_datagram_iovec"},
	}
	if bpf.HasKprobeFunction("tcp_v6_rcv") {
		attachOpts = append(attachOpts,
			bpf.AttachOption{ProgramName: "tcp_v6_rcv_prog", Symbol: "tcp_v6_rcv"})
	}
	if err := b.AttachWithOptions(attachOpts); err != nil {
		return errors.Join(err, reader.Close())
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
			var pd netRcvPerfEvent
			if err := reader.ReadInto(&pd); err != nil {
				return fmt.Errorf("read from perf event fail: %w", err)
			}

			containerID, ok := filterByConfigAndResolveContainerID(&pd, hostNetNsInode)
			if !ok {
				continue
			}

			where := latStageNames[pd.LatStage]
			lat := float64(pd.Latency) / 1000 / 1000 // ms
			latThreshold := latThresholds[pd.LatStage]
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
			_, found := matcher.Classify(cfg.IssuesList, title)
			if found {
				log.Debugf("net_rx_latency known issue")
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
			log.Debugf("net_rx_latency tracerData: %+v", tracerData)

			// save storage
			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:  "net_rx_latency",
				ContainerID: containerID,
				TracerTime:  time.Now(),
				TracerData:  tracerData,
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

func isQosExcluded(container *pod.Container) bool {
	for _, level := range cfg.NetRxLatency.ExcludedContainerQos {
		if strings.EqualFold(container.Qos.String(), level) {
			return true
		}
	}
	return false
}

func filterByConfigAndResolveContainerID(pd *netRcvPerfEvent, hostNetnsInode uint64) (string, bool) {
	inode := uint64(pd.NetNamespaceInode)

	if cfg.NetRxLatency.ExcludedHostNetnamespace && inode == hostNetnsInode {
		return "", false
	}

	var container *pod.Container

	if pd.NetNamespaceCookie != 0 {
		ct, err := pod.ContainerByNetCookie(pd.NetNamespaceCookie)
		if err != nil {
			log.Debugf("net_rx_latency: net_cookie lookup %d failed: %v", pd.NetNamespaceCookie, err)
		} else if ct != nil {
			container = ct
		}
	}

	if container == nil {
		ct, err := pod.ContainerByNetInode(inode)
		if err != nil {
			log.Warnf("net_rx_latency: get container by netns inode %d failed: %v", inode, err)
			return "", true
		}
		if ct == nil {
			return "", true
		}
		container = ct
	}

	if isQosExcluded(container) {
		return container.ID, false
	}
	return container.ID, true
}

func enableSkbTimestamp() (io.Closer, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create timestamp socket: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, unix.SO_TIMESTAMPING,
		unix.SOF_TIMESTAMPING_RX_SOFTWARE); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("enable skb rx timestamp: %w", err)
	}
	return fdCloser(fd), nil
}

type fdCloser int

func (f fdCloser) Close() error { return syscall.Close(int(f)) }
