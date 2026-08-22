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
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/net_tx_latency.c -o $BPF_DIR/net_tx_latency.o

const netTxLatencyStageCount = 3

var netTxLatencyStageNames = [netTxLatencyStageCount]string{
	"TX_STAGE_SENDMSG_QDISC",
	"TX_STAGE_QDISC_DEV",
	"TX_STAGE_DEV_NIC",
}

type netTxLatencyTracing struct {
	eventCount    [netTxLatencyStageCount]atomic.Uint64
	latencyNSSum  [netTxLatencyStageCount]atomic.Uint64
	lastLatencyNS [netTxLatencyStageCount]atomic.Uint64
}

func init() {
	tracing.RegisterEventTracing("net_tx_latency", newNetTxLatency)
}

func newNetTxLatency() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netTxLatencyTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *netTxLatencyTracing) Start(ctx context.Context) error {
	cfg := configSnapshot()
	thresholds := [netTxLatencyStageCount]uint64{
		cfg.NetTxLatency.Sendmsg2Qdisc,
		cfg.NetTxLatency.Qdisc2DevXmit,
		cfg.NetTxLatency.DevXmit2Nic,
	}
	for i, threshold := range thresholds {
		if threshold == 0 {
			return fmt.Errorf("net_tx_latency threshold %s must be greater than zero", netTxLatencyStageNames[i])
		}
	}

	args := map[string]any{
		"txlat_thresh_sendmsg_qdisc": thresholds[0] * uint64(time.Millisecond),
		"txlat_thresh_qdisc_dev":     thresholds[1] * uint64(time.Millisecond),
		"txlat_thresh_dev_nic":       thresholds[2] * uint64(time.Millisecond),
	}
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), args)
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

	b.DetachOnContextDone(childCtx, cancel)

	hostNetNamespaceInum, err := netutil.NetNamespaceInumByPID(1)
	if err != nil {
		return fmt.Errorf("get host netns inum: %w", err)
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var event abi.NetRXLatencyEvent
			if err := reader.ReadInto(&event); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("read net_tx_latency perf event: %w", err)
			}

			stage := int(event.LatencyStage)
			if stage >= len(netTxLatencyStageNames) {
				log.Warnf("net_tx_latency: unknown stage %d", stage)
				continue
			}

			eventConfig := configSnapshot()
			containerID, ok := filterNetLatencyEvent(
				event.NetNamespaceInum,
				event.NetNamespaceCookie,
				hostNetNamespaceInum,
				eventConfig.NetTxLatency.ExcludedHostNetnamespace,
				eventConfig.NetTxLatency.ExcludedContainerQos,
				"net_tx_latency",
			)
			if !ok {
				continue
			}

			c.recordMetric(stage, event.LatencyNS)
			tracerData := newNetTxTracingData(&event, thresholds[stage])
			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:  "net_tx_latency",
				ContainerID: containerID,
				TracerTime:  time.Now(),
				TracerData:  tracerData,
			}); err != nil {
				log.Warnf("failed to save net_tx_latency data: %v", err)
			}
		}
	}
}

func newNetTxTracingData(event *abi.NetRXLatencyEvent, threshold uint64) *NetTracingData {
	return &NetTracingData{
		Comm:               bytesutil.ToStr(event.Comm[:]),
		PID:                event.TGIDPID >> 32,
		LatencyStage:       netTxLatencyStageNames[event.LatencyStage],
		LatencyMS:          float64(event.LatencyNS) / float64(time.Millisecond),
		LatencyThresholdMS: threshold,
		NetdevName:         bytesutil.ToStr(event.NetdevName[:]),
		NetNamespaceInum:   event.NetNamespaceInum,
		NetNamespaceCookie: event.NetNamespaceCookie,
		TCPState:           packet.TCPStateName(event.TCPState),
		TCPSaddr:           netutil.Inetv4Ntop(event.TCPSaddr).String(),
		TCPDaddr:           netutil.Inetv4Ntop(event.TCPDaddr).String(),
		TCPSport:           netutil.Ntohs(event.TCPSport),
		TCPDport:           netutil.Ntohs(event.TCPDport),
		TCPSeq:             netutil.Ntohl(event.TCPSeq),
		TCPAckSeq:          netutil.Ntohl(event.TCPAckSeq),
		PacketLenBytes:     event.PacketLenBytes,
	}
}

func (c *netTxLatencyTracing) recordMetric(stage int, latencyNS uint64) {
	c.eventCount[stage].Add(1)
	c.latencyNSSum[stage].Add(latencyNS)
	c.lastLatencyNS[stage].Store(latencyNS)
}

func (c *netTxLatencyTracing) Update() ([]*metric.Data, error) {
	data := make([]*metric.Data, 0, netTxLatencyStageCount*3)
	for stage, stageName := range netTxLatencyStageNames {
		labels := map[string]string{"stage": stageName}
		data = append(data,
			metric.NewCounterData(
				"events_total",
				float64(c.eventCount[stage].Load()),
				"Number of TX latency threshold violations.",
				labels,
			),
			metric.NewCounterData(
				"seconds_total",
				float64(c.latencyNSSum[stage].Load())/float64(time.Second),
				"Total latency of TX threshold violations in seconds.",
				labels,
			),
			metric.NewGaugeData(
				"last_seconds",
				float64(c.lastLatencyNS[stage].Load())/float64(time.Second),
				"Most recently observed TX latency in seconds.",
				labels,
			),
		)
	}
	return data, nil
}
