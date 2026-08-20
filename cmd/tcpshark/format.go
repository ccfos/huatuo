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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/types"
)

const tcpFlagsSynAck = tcpFlagSYN | tcpFlagACK

const textEventBufferSize = 512

var retransmitEventTypeNames = map[abi.TCPRetransmitEventType]string{
	abi.TCPRetransmitEventSKB:    "tcp_retransmit_skb",
	abi.TCPRetransmitEventSynack: "tcp_retransmit_synack",
	abi.TCPRetransmitEventTlp:    "tcp_send_loss_probe",
}

// writer is the single write destination for a tcpshark session.
type writer interface {
	Write(ev *types.TCPRetransmitTracing) error
}

type textWriter struct{ w io.Writer }

func (s *textWriter) Write(ev *types.TCPRetransmitTracing) error {
	// Full events are normally below 512 bytes, so one allocation covers the
	// common hot path without retaining an unbounded buffer between events.
	line := make([]byte, 0, textEventBufferSize)
	line = append(line, ev.ObservedTimestamp...)
	line = append(line, " ["...)
	line = append(line, ev.Phase...)
	line = append(line, '/')
	line = append(line, ev.TCPReason...)
	line = append(line, "] "...)
	line = append(line, ev.TCPSaddr...)
	line = append(line, ':')
	line = strconv.AppendUint(line, uint64(ev.TCPSport), 10)
	line = append(line, " > "...)
	line = append(line, ev.TCPDaddr...)
	line = append(line, ':')
	line = strconv.AppendUint(line, uint64(ev.TCPDport), 10)
	line = append(line, " state="...)
	line = append(line, ev.TCPState...)
	line = append(line, " event_type="...)
	line = append(line, ev.EventType...)
	if ev.EventType == "tcp_retransmit_synack" {
		line = append(line, " [SYNACK]"...)
	}
	if ev.SkbAddr != "" {
		line = append(line, " skb="...)
		line = append(line, ev.SkbAddr...)
	}
	line = append(line, " seq="...)
	line = strconv.AppendUint(line, uint64(ev.TCPSeq), 10)
	if ev.TCPEndSeq != 0 {
		line = append(line, " end="...)
		line = strconv.AppendUint(line, uint64(ev.TCPEndSeq), 10)
	}
	line = append(line, " ack="...)
	line = strconv.AppendUint(line, uint64(ev.TCPAckSeq), 10)
	if ev.TCPFlags != "" {
		line = append(line, " flags="...)
		line = append(line, ev.TCPFlags...)
	}
	line = append(line, " pid="...)
	line = strconv.AppendUint(line, ev.PID, 10)
	line = append(line, " comm="...)
	line = append(line, ev.Comm...)
	line = append(line, " ca="...)
	line = strconv.AppendUint(line, uint64(ev.CaState), 10)
	line = append(line, " retrans="...)
	line = strconv.AppendUint(line, uint64(ev.IcskRetransmits), 10)
	line = append(line, " icsk_pending="...)
	line = strconv.AppendUint(line, uint64(ev.IcskPending), 10)
	if ev.ReordSeen != 0 {
		line = append(line, " reord_seen="...)
		line = strconv.AppendUint(line, uint64(ev.ReordSeen), 10)
	}
	if ev.DsackDups != 0 {
		line = append(line, " dsack_dups="...)
		line = strconv.AppendUint(line, uint64(ev.DsackDups), 10)
	}
	if ev.ContainerID != "" {
		line = append(line, " container_id="...)
		line = append(line, ev.ContainerID...)
	}
	if ev.MemoryCgroupCSSAddr != "" {
		line = append(line, " memory_cgroup_css_addr="...)
		line = append(line, ev.MemoryCgroupCSSAddr...)
	}
	if ev.NetNamespaceCookie != 0 {
		line = append(line, " net_namespace_cookie="...)
		line = strconv.AppendUint(line, ev.NetNamespaceCookie, 10)
	}
	if ev.NetNamespaceInum != 0 {
		line = append(line, " net_namespace_inum="...)
		line = strconv.AppendUint(line, uint64(ev.NetNamespaceInum), 10)
	}
	if ev.DropLocation != "" {
		line = append(line, " drop_location="...)
		line = append(line, ev.DropLocation...)
	}
	if ev.Source != "" {
		line = append(line, " source="...)
		line = append(line, ev.Source...)
	}
	line = append(line, '\n')

	n, err := s.w.Write(line)
	if err == nil && n != len(line) {
		return io.ErrShortWrite
	}
	return err
}

type jsonWriter struct{ w io.Writer }

func (s *jsonWriter) Write(ev *types.TCPRetransmitTracing) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := s.w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}

type socketWriter struct{ client *toolstream.Client }

func (s *socketWriter) Write(ev *types.TCPRetransmitTracing) error {
	return s.client.Send(ev)
}

type writerOptions struct {
	outputFormat string
	socketPath   string
	toolName     string
	version      string
	taskID       string
}

func newWriter(output io.Writer, options *writerOptions) (writer, func() error, error) {
	if options.socketPath != "" {
		client, err := toolstream.NewClient(toolstream.ClientOptions{
			SockPath: options.socketPath,
			ToolName: options.toolName,
			Version:  options.version,
			TaskID:   options.taskID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("--output-storage: %w", err)
		}
		return &socketWriter{client: client}, client.End, nil
	}
	if output == nil {
		return nil, nil, fmt.Errorf("output is nil")
	}

	switch options.outputFormat {
	case outputJSON:
		return &jsonWriter{w: output}, func() error { return nil }, nil
	case outputText:
		return &textWriter{w: output}, func() error { return nil }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported output %q", options.outputFormat)
	}
}

func formatEvent(
	ev *abi.TCPRetransmitEvent,
	sourceType string,
) *types.TCPRetransmitTracing {
	rawEventType := abi.TCPRetransmitEventType(ev.EventType)
	tcpFlagsRaw := ev.TCPFlags
	if rawEventType == abi.TCPRetransmitEventSynack {
		tcpFlagsRaw = tcpFlagsSynAck
	}
	tcpFlags := packet.TCPFlagStrings[tcpFlagsRaw]

	classification := classifyRetransmit(ev)
	eventType, ok := retransmitEventTypeNames[rawEventType]
	if !ok {
		eventType = "unknown"
	}

	var saddr, daddr string
	switch ev.Family {
	case unix.AF_INET:
		saddr = net.IP(ev.Saddr[:net.IPv4len]).String()
		daddr = net.IP(ev.Daddr[:net.IPv4len]).String()
	case unix.AF_INET6:
		saddr = net.IP(ev.Saddr[:]).String()
		daddr = net.IP(ev.Daddr[:]).String()
	}

	return &types.TCPRetransmitTracing{
		ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TCPReason:           classification.reason.String(),
		Source:              sourceType,
		Comm:                bytesutil.ToStr(ev.Comm[:]),
		PID:                 ev.TGIDPID >> 32,
		MemoryCgroupCSSAddr: kernaddr.Format(ev.MemcgCSSAddr),
		NetNamespaceCookie:  ev.NetNamespaceCookie,
		NetNamespaceInum:    ev.NetNamespaceInum,
		TCPState:            packet.TCPStateName(uint8(ev.State)),
		TCPSaddr:            saddr,
		TCPDaddr:            daddr,
		TCPSport:            ev.Sport,
		TCPDport:            ev.Dport,
		TCPSeq:              ev.TCPSeq,
		TCPAckSeq:           ev.TCPAck,
		TCPEndSeq:           ev.TCPEndSeq,
		TCPFlags:            tcpFlags,
		Phase:               classification.phase.String(),
		EventType:           eventType,
		CaState:             ev.CaState,
		IcskRetransmits:     ev.IcskRetransmits,
		IcskPending:         ev.IcskPending,
		ReordSeen:           ev.ReordSeen,
		DsackDups:           ev.DsackDups,
		SkbAddr:             kernaddr.Format(ev.SKBAddr),
	}
}
