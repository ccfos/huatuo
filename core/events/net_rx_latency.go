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
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
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
	PID                uint64  `json:"pid"`
	LatencyStage       string  `json:"latency_stage"`
	LatencyMS          float64 `json:"latency_ms"`
	LatencyThresholdMS uint64  `json:"latency_threshold_ms"`
	NetdevName         string  `json:"netdev_name"`
	NetNamespaceInum   uint32  `json:"net_namespace_inum"`
	NetNamespaceCookie uint64  `json:"net_namespace_cookie"`
	TCPState           string  `json:"tcp_state"`
	TCPSaddr           string  `json:"tcp_saddr"`
	TCPDaddr           string  `json:"tcp_daddr"`
	TCPSport           uint16  `json:"tcp_sport"`
	TCPDport           uint16  `json:"tcp_dport"`
	TCPSeq             uint32  `json:"tcp_seq"`
	TCPAckSeq          uint32  `json:"tcp_ack_seq"`
	PacketLenBytes     uint64  `json:"packet_len_bytes"`
}

var latencyStageNames = []string{
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
	cfg := configSnapshot()
	rxlatThreshNetif := cfg.NetRxLatency.Driver2NetRx        // ms, before RPS to a core recv(__netif_receive_skb)
	rxlatThreshTcpv4 := cfg.NetRxLatency.Driver2TCP          // ms, before RPS to TCP recv(tcp_v4_rcv)
	rxlatThreshUsercopy := cfg.NetRxLatency.Driver2Userspace // ms, before RPS to user recv(skb_copy_datagram_iovec)

	if rxlatThreshNetif == 0 || rxlatThreshTcpv4 == 0 || rxlatThreshUsercopy == 0 {
		return fmt.Errorf("net_rx_latency threshold [%v %v %v]ms invalid", rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy)
	}

	log.Debugf("net_rx_latency start, latency threshold [%v %v %v]ms", rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy)

	latencyThresholds := []uint64{rxlatThreshNetif, rxlatThreshTcpv4, rxlatThreshUsercopy}

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
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), args)
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

	b.DetachOnContextDone(childCtx, cancel)

	// save host netns
	hostNetNamespaceInum, err := netutil.NetNamespaceInumByPID(1)
	if err != nil {
		return fmt.Errorf("get host netns inum: %w", err)
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var pd abi.NetRXLatencyEvent
			if err := reader.ReadInto(&pd); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("read from perf event fail: %w", err)
			}

			eventConfig := configSnapshot()
			containerID, ok := filterByConfigAndResolveContainerID(
				&pd,
				hostNetNamespaceInum,
				eventConfig,
			)
			if !ok {
				continue
			}

			latencyStage := latencyStageNames[pd.LatencyStage]
			latencyMS := float64(pd.LatencyNS) / 1000 / 1000
			latencyThresholdMS := latencyThresholds[pd.LatencyStage]
			state := packet.TCPStateName(pd.TCPState)
			saddr, daddr := netutil.Inetv4Ntop(pd.TCPSaddr).String(), netutil.Inetv4Ntop(pd.TCPDaddr).String()
			sport, dport := netutil.Ntohs(pd.TCPSport), netutil.Ntohs(pd.TCPDport)
			seq, ackSeq := netutil.Ntohl(pd.TCPSeq), netutil.Ntohl(pd.TCPAckSeq)
			packetLenBytes := pd.PacketLenBytes

			comm := bytesutil.ToStr(pd.Comm[:])
			pid := pd.TGIDPID >> 32

			title := fmt.Sprintf("comm=%s:%d to=%s lat(ms)=%.2f state=%s saddr=%s sport=%d daddr=%s dport=%d seq=%d ackSeq=%d packetLenBytes=%d",
				comm, pid, latencyStage, latencyMS, state, saddr, sport, daddr, dport, seq, ackSeq, packetLenBytes)

			// known issue filter
			_, found := matcher.Classify(eventConfig.IssuesList, title)
			if found {
				log.Debugf("net_rx_latency known issue")
				continue
			}

			tracerData := &NetTracingData{
				Comm:               comm,
				PID:                pid,
				LatencyStage:       latencyStage,
				LatencyMS:          latencyMS,
				LatencyThresholdMS: latencyThresholdMS,
				NetdevName:         bytesutil.ToStr(pd.NetdevName[:]),
				NetNamespaceInum:   pd.NetNamespaceInum,
				NetNamespaceCookie: pd.NetNamespaceCookie,
				TCPState:           state,
				TCPSaddr:           saddr,
				TCPDaddr:           daddr,
				TCPSport:           sport,
				TCPDport:           dport,
				TCPSeq:             seq,
				TCPAckSeq:          ackSeq,
				PacketLenBytes:     packetLenBytes,
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

func isQosExcluded(container *pod.Container, cfg *Config) bool {
	for _, level := range cfg.NetRxLatency.ExcludedContainerQos {
		if strings.EqualFold(container.Qos.String(), level) {
			return true
		}
	}
	return false
}

func filterByConfigAndResolveContainerID(
	pd *abi.NetRXLatencyEvent,
	hostNetNamespaceInum uint64,
	cfg *Config,
) (string, bool) {
	inum := uint64(pd.NetNamespaceInum)

	if cfg.NetRxLatency.ExcludedHostNetnamespace && inum == hostNetNamespaceInum {
		return "", false
	}

	var container *pod.Container

	if pd.NetNamespaceCookie != 0 {
		ct, err := pod.ContainerByNetNamespaceCookie(pd.NetNamespaceCookie)
		if err != nil {
			log.Debugf("net_rx_latency: netns_cookie lookup %d failed: %v", pd.NetNamespaceCookie, err)
		} else if ct != nil {
			container = ct
		}
	}

	if container == nil {
		ct, err := pod.ContainerByNetNamespaceInum(inum)
		if err != nil {
			log.Warnf("net_rx_latency: get container by netns inum %d failed: %v", inum, err)
			return "", true
		}
		if ct == nil {
			return "", true
		}
		container = ct
	}

	if isQosExcluded(container, cfg) {
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
