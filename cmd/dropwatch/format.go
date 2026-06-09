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

	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/types"
)

// writer is the single write destination for a dropwatch session.
type writer interface {
	Write(ev *types.DropWatchTracing) error
}

type textWriter struct{ w io.Writer }

func (s *textWriter) Write(ev *types.DropWatchTracing) error {
	fmt.Fprintf(s.w, "%s %s %s len=%d dev=%s pid=%d[%s] addr=%s\n",
		ev.ObservedTimestamp, ev.PacketType, formatDetail(ev.PacketInfo),
		ev.PacketLen, ev.NetdevName, ev.Pid, ev.Comm, ev.PacketSkbAddr)

	if ev.Stack != "" {
		for i, frame := range strings.Split(strings.TrimRight(ev.Stack, "\n"), "\n") {
			if frame != "" {
				fmt.Fprintf(s.w, "\t#%-2d  %s\n", i, frame)
			}
		}
	}

	return nil
}

type jsonWriter struct{ w io.Writer }

func (s *jsonWriter) Write(ev *types.DropWatchTracing) error {
	b, _ := json.Marshal(ev)
	_, _ = fmt.Fprintf(s.w, "%s\n", b)
	return nil
}

type socketWriter struct{ client *toolstream.Client }

func (s *socketWriter) Write(ev *types.DropWatchTracing) error {
	ev.Source = "client"
	return s.client.Send(ev)
}

// newWriter returns the appropriate writer based on flags. client may be nil.
func newWriter(outputFmt string, client *toolstream.Client) writer {
	switch {
	case client != nil:
		return &socketWriter{client: client}
	case outputFmt == "json":
		return &jsonWriter{w: os.Stdout}
	default:
		return &textWriter{w: os.Stdout}
	}
}

type detailable interface{ Detail() string }

func formatDetail(info packet.PacketInfo) string {
	if d, ok := info.(detailable); ok {
		return d.Detail()
	}
	return "[?]"
}
