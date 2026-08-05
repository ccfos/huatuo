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
	"strings"
	"time"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/linkstatus"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/types"
)

// writer is the single write destination for a dropwatch session.
type writer interface {
	Write(ev *types.DropWatchTracing) error
}

type textWriter struct{ w io.Writer }

func (s *textWriter) Write(ev *types.DropWatchTracing) error {
	line := fmt.Sprintf("%s %s reason=%s len=%d dev=%s pid=%d[%s] addr=%s source=%s\n",
		ev.ObservedTimestamp, ev.Layers, ev.DropReason,
		ev.PacketLen, ev.NetdevName, ev.Pid, ev.Comm, ev.PacketSkbAddr, ev.Source)
	n, err := io.WriteString(s.w, line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}

	if err := symbol.FormatStackLines(s.w, ev.Stack); err != nil {
		return err
	}

	return nil
}

type jsonWriter struct{ w io.Writer }

func (s *jsonWriter) Write(ev *types.DropWatchTracing) error {
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

func (s *socketWriter) Write(ev *types.DropWatchTracing) error {
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
			return nil, nil, fmt.Errorf("create event sink: %w", err)
		}
		return &socketWriter{client: client}, client.End, nil
	}

	switch options.outputFormat {
	case "json":
		return &jsonWriter{w: output}, func() error { return nil }, nil
	case "text":
		return &textWriter{w: output}, func() error { return nil }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported output format %q", options.outputFormat)
	}
}

func formatEvent(ev *abi.DropwatchPacketEvent, names dropReasonNames, sourceType string) *types.DropWatchTracing {
	pkt := packet.Hdr{
		EthProto:  ev.PktHdr.EthProto,
		RawLen:    uint8(ev.PktHdr.RawLen),
		HasEthHdr: uint8(ev.PktHdr.HasEthHdr),
		SkState:   uint8(ev.PktHdr.SkState),
		Raw:       ev.PktHdr.Raw,
	}

	p, err := packet.Parse(&pkt)
	if err != nil {
		log.WithError(err).Debug("parse dropwatch packet")
	}

	frames := symbol.KsymStackStrs(ev.Stack[:], symbol.KsymStackMaxDepth)
	stackStr := strings.Join(frames, "\n")

	return &types.DropWatchTracing{
		ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		DropReason:          names.resolve(ev.Meta.DropReason),
		Comm:                bytesutil.ToStr(ev.Meta.Comm[:]),
		Pid:                 ev.Meta.TGIDPID >> 32,
		MemoryCgroupCSSAddr: kernaddr.Format(ev.Meta.MemcgCSSAddr),
		NetNamespaceCookie:  ev.Meta.NetNSCookie,
		NetNamespaceInum:    ev.Meta.NetNSInum,
		NetdevName:          bytesutil.ToStr(ev.Meta.DevName[:]),
		NetdevIfindex:       ev.Meta.Ifindex,
		NetdevQueueMapping:  ev.Meta.QueueMapping,
		NetdevLinkStatus:    linkstatus.FlagsRaw(ev.Meta.DevFlags),
		PacketSkbAddr:       kernaddr.Format(ev.Meta.KfreeSKBAddr),
		PacketEthProto:      fmt.Sprintf("0x%04x", ev.PktHdr.EthProto),
		PacketLen:           ev.PktHdr.PktLen,
		Layers:              p,
		Stack:               stackStr,
		Source:              sourceType,
	}
}
