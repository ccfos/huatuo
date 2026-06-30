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
	"os"
	"strings"
	"time"

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
	if _, err := fmt.Fprintf(s.w, "%s %s len=%d dev=%s pid=%d[%s] addr=%s\n",
		ev.ObservedTimestamp, ev.Layers,
		ev.PacketLen, ev.NetdevName, ev.Pid, ev.Comm, ev.PacketSkbAddr); err != nil {
		return err
	}

	if ev.Stack != "" {
		if err := symbol.FormatStackLines(s.w, ev.Stack); err != nil {
			return err
		}
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
	_, err = s.w.Write(b)
	return err
}

type socketWriter struct{ client *toolstream.Client }

func (s *socketWriter) Write(ev *types.DropWatchTracing) error {
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
			return nil, nil, fmt.Errorf("dropwatch: --output-storage: %w", err)
		}
		return &socketWriter{client: client}, client.End, nil
	}

	switch opt.outputFmt {
	case "json":
		return &jsonWriter{w: os.Stdout}, func() {}, nil
	default:
		return &textWriter{w: os.Stdout}, func() {}, nil
	}
}

func formatEvent(ev *dropPacketEvent) *types.DropWatchTracing {
	pkt := packet.Hdr{
		EthProto:  ev.Raw.EthProto,
		RawLen:    uint8(ev.Raw.RawLen),
		HasEthHdr: uint8(ev.Raw.HasEthHdr),
		SkState:   uint8(ev.Raw.SkState),
		Raw:       ev.Raw.Raw,
	}

	p, err := packet.Parse(&pkt)
	if err != nil {
		log.Debugf("dropwatch: parse packet: %v", err)
	}

	frames := symbol.KsymStackStrs(ev.Stack[:], symbol.KsymStackMaxDepth)
	stackStr := strings.Join(frames, "\n")

	return &types.DropWatchTracing{
		ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Comm:                bytesutil.ToStr(ev.Meta.Comm[:]),
		Pid:                 ev.Meta.TgidPid >> 32,
		MemoryCgroupCSSAddr: kernaddr.Format(ev.Meta.MemoryCgroupCSSAddr),
		NetNamespaceCookie:  ev.Meta.NetCookie,
		NetNamespaceInode:   ev.Meta.NetInode,
		NetdevName:          bytesutil.ToStr(ev.Meta.NetdevName[:]),
		NetdevIfindex:       ev.Meta.NetdevIfindex,
		NetdevQueueMapping:  ev.Meta.NetdevQueueMapping,
		NetdevLinkStatus:    linkstatus.FlagsRaw(ev.Meta.NetdevFlags),
		PacketSkbAddr:       kernaddr.Format(ev.Meta.SkbAddr),
		PacketEthProto:      fmt.Sprintf("0x%04x", ev.Raw.EthProto),
		PacketLen:           ev.Raw.PktLen,
		Layers:              p,
		Stack:               stackStr,
	}
}
