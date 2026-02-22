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
	"net"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/linkstatus"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/tracing"
)

const (
	tracerName = "dropwatch"
	logPrefix  = tracerName + ": "

	// type
	typeTCPCommonDrop               = 1
	typeTCPSynFlood                 = 2
	typeTCPListenOverflowHandshake1 = 3
	typeTCPListenOverflowHandshake3 = 4
)

// from include/net/tcp_states.h
var tcpstateMap = []string{
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

var typeMap = map[uint8]string{
	typeTCPCommonDrop:               "common_drop",
	typeTCPSynFlood:                 "syn_flood",
	typeTCPListenOverflowHandshake1: "listen_overflow_handshake1",
	typeTCPListenOverflowHandshake3: "listen_overflow_handshake3",
}

type perfEventT struct {
	TgidPid            uint64                              `json:"tgid_pid"`
	Saddr              uint32                              `json:"saddr"`
	Daddr              uint32                              `json:"daddr"`
	Sport              uint16                              `json:"sport"`
	Dport              uint16                              `json:"dport"`
	Seq                uint32                              `json:"seq"`
	AckSeq             uint32                              `json:"ack_seq"`
	PktLen             uint32                              `json:"pkt_len"`
	StackSize          int64                               `json:"stack_size"`
	Stack              [symbol.KsymbolStackMaxDepth]uint64 `json:"stack"`
	NetdevQueueMapping uint32                              `json:"netdev_queue_mapping"`
	NetdevFlags        uint32                              `json:"netdev_flags"`
	NetdevName         [bpf.NetdeviceLenSize]byte          `json:"netdev_name"`
	NetdevIfindex      uint32                              `json:"netdev_ifindex"`
	SkMaxAckBacklog    uint32                              `json:"sk_max_ack_backlog"`
	SkState            uint8                               `json:"sk_state"`
	Type               uint8                               `json:"type"`
	Pad0               uint16                              `json:"pad0"`
	Pad1               uint32                              `json:"pad1"`
	NetCookie          uint64                              `json:"net_cookie"`
	Comm               [bpf.TaskCommLen]byte               `json:"comm"`
}

type DropWatchTracingData struct {
	Type               string   `json:"type"`
	Comm               string   `json:"comm"`
	Pid                uint64   `json:"pid"`
	Saddr              string   `json:"saddr"`
	Daddr              string   `json:"daddr"`
	Sport              uint16   `json:"sport"`
	Dport              uint16   `json:"dport"`
	SrcHostname        string   `json:"src_hostname"`
	DestHostname       string   `json:"dest_hostname"`
	MaxAckBacklog      uint32   `json:"max_ack_backlog"`
	Seq                uint32   `json:"seq"`
	AckSeq             uint32   `json:"ack_seq"`
	PktLen             uint32   `json:"pkt_len"`
	SkState            string   `json:"sk_state"`
	Stack              string   `json:"stack"`
	NetdevQueueMapping uint32   `json:"netdev_queue_mapping"`
	NetdevLinkStatus   []string `json:"netdev_linkstatus"`
	NetdevName         string   `json:"netdev_name"`
	NetdevIfindex      uint32   `json:"netdev_ifindex"`
	NetCookie          uint64   `json:"net_cookie"`
}

type dropWatchTracing struct{}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/dropwatch.c -o $BPF_DIR/dropwatch.o

func init() {
	tracing.RegisterEventTracing(tracerName, newDropWatch)
}

func newDropWatch() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &dropWatchTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

// Start starts the tracer.
func (c *dropWatchTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// attach
	reader, err := b.AttachAndEventPipe(childCtx, "perf_events", 8192)
	if err != nil {
		return fmt.Errorf("attach and event pipe: %w", err)
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			log.Info(logPrefix + "tracer is stopped.")
			return nil
		default:
			var event perfEventT
			if err := reader.ReadInto(&event); err != nil {
				return fmt.Errorf(logPrefix+"failed to read from perf: %w", err)
			}

			// format
			tracerData := c.formatEvent(&event)

			if c.ignore(tracerData) {
				log.Debugf(logPrefix+"ignore dropwatch data: %v", tracerData)
				continue
			}

			storage.Save(tracerName, "", time.Now(), tracerData)
		}
	}
}

func (c *dropWatchTracing) formatEvent(event *perfEventT) *DropWatchTracingData {
	// hostname
	saddr := netutil.Inetv4Ntop(event.Saddr).String()
	daddr := netutil.Inetv4Ntop(event.Daddr).String()
	srcHostname := "<nil>"
	destHostname := "<nil>"
	h, err := net.LookupAddr(saddr)
	if err == nil && len(h) > 0 {
		srcHostname = h[0]
	}

	h, err = net.LookupAddr(daddr)
	if err == nil && len(h) > 0 {
		destHostname = h[0]
	}

	// stack
	stacks := strings.Join(symbol.DumpKernelBackTrace(event.Stack[:], symbol.KsymbolStackMaxDepth).BackTrace, "\n")

	// tracer data
	data := &DropWatchTracingData{
		Type:               typeMap[event.Type],
		Comm:               bytesutil.ToString(event.Comm[:]),
		Pid:                event.TgidPid >> 32,
		Saddr:              saddr,
		Daddr:              daddr,
		Sport:              netutil.Ntohs(event.Sport),
		Dport:              netutil.Ntohs(event.Dport),
		SrcHostname:        srcHostname,
		DestHostname:       destHostname,
		Seq:                netutil.Ntohl(event.Seq),
		AckSeq:             netutil.Ntohl(event.AckSeq),
		PktLen:             event.PktLen,
		SkState:            tcpstateMap[event.SkState],
		Stack:              stacks,
		MaxAckBacklog:      event.SkMaxAckBacklog,
		NetdevQueueMapping: event.NetdevQueueMapping,
		NetdevName:         bytesutil.ToString(event.NetdevName[:]),
		NetdevLinkStatus:   linkstatus.FlagsRaw(event.NetdevFlags),
		NetdevIfindex:      event.NetdevIfindex,
		NetCookie:          event.NetCookie,
	}

	log.Debugf(logPrefix+"tracing data: %v", data)
	return data
}

func (c *dropWatchTracing) ignore(data *DropWatchTracingData) bool {
	stack := strings.Split(data.Stack, "\n")
	// state: CLOSE_WAIT
	// stack:
	//	1. kfree_skb/ffffffff963047b0
	//	2. kfree_skb/ffffffff963047b0
	//	3. skb_rbtree_purge/ffffffff963089e0
	//	4. tcp_fin/ffffffff963ac200
	//	5. ...
	if data.SkState == "CLOSE_WAIT" {
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
	if conf.Get().EventTracing.Dropwatch.ExcludedNeighInvalidate {
		if len(stack) >= 3 && strings.HasPrefix(stack[2], "neigh_invalidate/") {
			return true
		}
	}

	// stack:
	// 1. kfree_skb/ffffffff82283d10
	// 2. kfree_skb/ffffffff82283d10
	// 3. bnxt_tx_int/ffffffffc05c6f20
	// 4. __bnxt_poll_work_done/ffffffffc05c50c0
	// 5. ...

	// stack:
	// 1. kfree_skb/ffffffffaba83d10
	// 2. kfree_skb/ffffffffaba83d10
	// 3. __bnxt_tx_int/ffffffffc045df90
	// 4. bnxt_tx_int/ffffffffc045e250
	// 5. ...
	if len(stack) >= 3 &&
		(strings.HasPrefix(stack[2], "bnxt_tx_int/") || strings.HasPrefix(stack[2], "__bnxt_tx_int/")) {
		return true
	}

	// default: false
	return false
}
