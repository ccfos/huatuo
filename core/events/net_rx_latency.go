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
	"io"
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
	Comm          string  `json:"comm"`
	Pid           uint64  `json:"pid"`
	Where         string  `json:"where"`
	Latency       float64 `json:"latency_ms"`
	LatThresholds uint64  `json:"lat_thresholds"`
	NetdevName    string  `json:"netdev_name"`
	NetnsInum     uint32  `json:"netns_inum"`
	State         string  `json:"state"`
	Saddr         string  `json:"saddr"`
	Daddr         string  `json:"daddr"`
	Sport         uint16  `json:"sport"`
	Dport         uint16  `json:"dport"`
	Seq           uint32  `json:"seq"`
	AckSeq        uint32  `json:"ack_seq"`
	PktLen        uint64  `json:"pkt_len"`
}

// from bpf perf
type netRcvPerfEvent struct {
	Comm       [bpf.TaskCommLen]byte
	Latency    uint64
	TgidPid    uint64
	PktLen     uint64
	Sport      uint16
	Dport      uint16
	Saddr      uint32
	Daddr      uint32
	Seq        uint32
	AckSeq     uint32
	State      uint8
	Where      uint8
	_          [2]byte
	NetdevName [bpf.NetdevNameLen]byte
	NetnsInum  uint32
}

var toWhere = []string{
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

	reader, err := b.AttachAndEventPipe(childCtx, "net_recv_lat_event_map", 8192)
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
			var pd netRcvPerfEvent
			if err := reader.ReadInto(&pd); err != nil {
				return fmt.Errorf("read from perf event fail: %w", err)
			}

			containerID, ok := resolveContainerID(&pd, hostNetNsInode)
			if !ok {
				continue
			}

			where := toWhere[pd.Where]
			lat := float64(pd.Latency) / 1000 / 1000 // ms
			latThreshold := latThresholds[pd.Where]
			state := packet.TCPStateName(pd.State)
			saddr, daddr := netutil.Inetv4Ntop(pd.Saddr).String(), netutil.Inetv4Ntop(pd.Daddr).String()
			sport, dport := netutil.Ntohs(pd.Sport), netutil.Ntohs(pd.Dport)
			seq, ackSeq := netutil.Ntohl(pd.Seq), netutil.Ntohl(pd.AckSeq)
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
				Comm:          comm,
				Pid:           pid,
				Where:         where,
				Latency:       lat,
				LatThresholds: latThreshold,
				NetdevName:    bytesutil.ToStr(pd.NetdevName[:]),
				NetnsInum:     pd.NetnsInum,
				State:         state,
				Saddr:         saddr,
				Daddr:         daddr,
				Sport:         sport,
				Dport:         dport,
				Seq:           seq,
				AckSeq:        ackSeq,
				PktLen:        pktLen,
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

func resolveContainerID(pd *netRcvPerfEvent, hostNetnsInode uint64) (string, bool) {
	inode := uint64(pd.NetnsInum)

	if cfg.NetRxLatency.ExcludedHostNetnamespace && inode == hostNetnsInode {
		return "", false
	}

	container, err := pod.ContainerByNetInode(inode)
	if err != nil {
		log.Warnf("net_rx_latency: get container by netns inode %d failed: %v", inode, err)
		return "", true
	}
	if container == nil {
		return "", true
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
