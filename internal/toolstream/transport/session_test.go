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

package transport

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	capnp "capnproto.org/go/capnp/v3"
)

func TestParseSessionMetadata(t *testing.T) {
	for _, tt := range []struct {
		name string
		want Session
	}{
		{name: "complete", want: Session{ToolName: "dropwatch", Version: "1.0", TaskID: "task-1"}},
		{name: "empty optional fields", want: Session{ToolName: "dropwatch"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg, _ := newSessionMessage(t, tt.want)
			got, err := parseSession(msg)
			if err != nil {
				t.Fatalf("parseSession() error = %v", err)
			}
			if got == nil || *got != tt.want {
				t.Fatalf("parseSession() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSessionMalformedMetadata(t *testing.T) {
	for index, field := range []string{"toolName", "version", "taskID"} {
		t.Run(field, func(t *testing.T) {
			msg, pointerStart := newSessionMessage(t, Session{
				ToolName: "dropwatch", Version: "1.0", TaskID: "task-1",
			})
			seg, err := msg.Segment(0)
			if err != nil {
				t.Fatal(err)
			}

			// A far pointer to a nonexistent segment forces a decoding error,
			// unlike a null pointer, which represents valid empty text.
			offset := pointerStart + index*8
			binary.LittleEndian.PutUint64(seg.Data()[offset:offset+8], 2|(uint64(1)<<32))

			got, err := parseSession(msg)
			if got != nil {
				t.Fatalf("parseSession() = %+v, want nil session", got)
			}
			prefix := "transport: decode connect " + field + ": "
			if err == nil || !strings.HasPrefix(err.Error(), prefix) {
				t.Fatalf("parseSession() error = %v, want prefix %q", err, prefix)
			}
			if errors.Unwrap(err) == nil {
				t.Fatalf("parseSession() error = %v, want wrapped decoding error", err)
			}
		})
	}
}

func newSessionMessage(t *testing.T, metadata Session) (*capnp.Message, int) {
	t.Helper()
	msg, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(msg.Release)
	root, err := NewRootMessage(seg)
	if err != nil {
		t.Fatal(err)
	}

	// A fresh single-segment arena appends ConnectRequest at this offset.
	// Its pointer section follows the generated struct's data section.
	connectStart := len(seg.Data())
	connect, err := root.NewConnect()
	if err != nil {
		t.Fatal(err)
	}
	pointerStart := connectStart + int(capnp.Struct(connect).Size().DataSize)
	if err := connect.SetToolName(metadata.ToolName); err != nil {
		t.Fatal(err)
	}
	if err := connect.SetVersion(metadata.Version); err != nil {
		t.Fatal(err)
	}
	if err := connect.SetTaskID(metadata.TaskID); err != nil {
		t.Fatal(err)
	}
	return msg, pointerStart
}
