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
	"os"
	"time"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/types"
)

const tcpFlagsSynAck uint8 = 0x12 // SYN(0x02) | ACK(0x10)

// writer is the single write destination for a tcpshark session.
type writer interface {
	Write(ev *types.TCPRetransmitTracing) error
}

type textWriter struct{ w io.Writer }

func (s *textWriter) Write(ev *types.TCPRetransmitTracing) error {
	detail := fmt.Sprintf(
		"[%s/%s] %s:%d > %s:%d state=%s event_type=%s",
		ev.Phase,
		ev.TCPReason,
		ev.TCPSaddr,
		ev.TCPSport,
		ev.TCPDaddr,
		ev.TCPDport,
		ev.TCPState,
		ev.EventType,
	)
	if ev.EventType == "tcp_retransmit_synack" {
		detail += " [SYNACK]"
	}
	if ev.SkbAddr != "" {
		detail += " skb=" + ev.SkbAddr
	}
	detail += fmt.Sprintf(" seq=%d", ev.TCPSeq)
	if ev.TCPEndSeq != 0 {
		detail += fmt.Sprintf(" end=%d", ev.TCPEndSeq)
	}
	detail += fmt.Sprintf(" ack=%d", ev.TCPAckSeq)
	if ev.TCPFlags != "" {
		detail += " flags=" + ev.TCPFlags
	}
	detail += fmt.Sprintf(
		" pid=%d comm=%s ca=%d retrans=%d icsk_pending=%d",
		ev.Pid,
		ev.Comm,
		ev.CaState,
		ev.IcskRetransmits,
		ev.IcskPending,
	)
	if ev.ReordSeen != 0 {
		detail += fmt.Sprintf(" reord_seen=%d", ev.ReordSeen)
	}
	if ev.DsackDups != 0 {
		detail += fmt.Sprintf(" dsack_dups=%d", ev.DsackDups)
	}
	if ev.ContainerID != "" {
		detail += " container_id=" + ev.ContainerID
	}
	if ev.MemoryCgroupCSSAddr != "" {
		detail += " memory_cgroup_css_addr=" + ev.MemoryCgroupCSSAddr
	}
	if ev.NetNamespaceCookie != 0 {
		detail += fmt.Sprintf(" net_namespace_cookie=%d", ev.NetNamespaceCookie)
	}
	if ev.NetNamespaceInum != 0 {
		detail += fmt.Sprintf(" net_namespace_inum=%d", ev.NetNamespaceInum)
	}
	if ev.DropLocation != "" {
		detail += " drop_location=" + ev.DropLocation
	}
	if ev.Source != "" {
		detail += " source=" + ev.Source
	}
	_, err := fmt.Fprintf(s.w, "%s %s\n", ev.ObservedTimestamp, detail)
	return err
}

type jsonWriter struct{ w io.Writer }

func (s *jsonWriter) Write(ev *types.TCPRetransmitTracing) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = s.w.Write(b)
	return err
}

type socketWriter struct{ client *toolstream.Client }

func (s *socketWriter) Write(ev *types.TCPRetransmitTracing) error {
	return s.client.Send(ev)
}

type writerOption struct {
	outputFmt string
	sockPath  string
	toolName  string
	version   string
	taskID    string
}

func newWriter(opt *writerOption) (writer, func(), error) {
	if opt.sockPath != "" {
		client, err := toolstream.NewClient(toolstream.ClientOptions{
			SockPath: opt.sockPath,
			ToolName: opt.toolName,
			Version:  opt.version,
			TaskID:   opt.taskID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("tcpshark: --output-storage: %w", err)
		}
		return &socketWriter{client: client}, client.End, nil
	}

	switch opt.outputFmt {
	case outputJSON:
		return &jsonWriter{w: os.Stdout}, func() {}, nil
	default:
		return &textWriter{w: os.Stdout}, func() {}, nil
	}
}

func formatEvent(ev *abi.TCPRetransmitEvent) *types.TCPRetransmitTracing {
	tcpFlagsRaw := ev.TCPFlags
	if ev.EventType == tcpRetransmitEventSYNACK {
		tcpFlagsRaw = tcpFlagsSynAck
	}
	tcpFlags := packet.TCPFlagStrings[tcpFlagsRaw]
	phase, reason := classifyEvent(ev, tcpFlags)

	var saddr, daddr string
	switch ev.Family {
	case unix.AF_INET:
		saddr = net.IP(ev.Saddr[:net.IPv4len]).String()
		daddr = net.IP(ev.Daddr[:net.IPv4len]).String()
	case unix.AF_INET6:
		saddr = net.IP(ev.Saddr[:]).String()
		daddr = net.IP(ev.Daddr[:]).String()
	}

	eventTypeStr := "unknown"
	switch ev.EventType {
	case tcpRetransmitEventSKB:
		eventTypeStr = "tcp_retransmit_skb"
	case tcpRetransmitEventSYNACK:
		eventTypeStr = "tcp_retransmit_synack"
	case tcpRetransmitEventTLP:
		eventTypeStr = "tcp_send_loss_probe"
	}

	return &types.TCPRetransmitTracing{
		ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		TCPReason:           reason.String(),
		Comm:                bytesutil.ToStr(ev.Comm[:]),
		Pid:                 ev.TGIDPID >> 32,
		MemoryCgroupCSSAddr: kernaddr.Format(ev.MemcgCSSAddr),
		NetNamespaceCookie:  ev.NetNSCookie,
		NetNamespaceInum:    ev.NetNSInum,
		TCPState:            tcpStateNameRaw(uint8(ev.State)),
		TCPSaddr:            saddr,
		TCPDaddr:            daddr,
		TCPSport:            ev.Sport,
		TCPDport:            ev.Dport,
		TCPSeq:              ev.TCPSeq,
		TCPAckSeq:           ev.TCPAck,
		TCPEndSeq:           ev.TCPEndSeq,
		TCPFlags:            tcpFlags,
		Phase:               phase.String(),
		EventType:           eventTypeStr,
		CaState:             ev.CaState,
		IcskRetransmits:     ev.IcskRetransmits,
		IcskPending:         ev.IcskPending,
		ReordSeen:           ev.ReordSeen,
		DsackDups:           ev.DsackDups,
		SkbAddr:             kernaddr.Format(ev.SKBAddr),
	}
}

func tcpStateNameRaw(state uint8) string {
	names := []string{
		"unknown", "ESTABLISHED", "SYN_SENT", "SYN_RECV",
		"FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT", "CLOSE",
		"CLOSE_WAIT", "LAST_ACK", "LISTEN", "CLOSING", "NEW_SYN_RECV",
	}
	if int(state) < len(names) {
		return names[state]
	}
	return fmt.Sprintf("UNKNOWN(%d)", state)
}
