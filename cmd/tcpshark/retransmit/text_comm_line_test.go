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

package retransmit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestTextWriterKeepsCommOnOneLine(t *testing.T) {
	var output bytes.Buffer
	if err := (&textWriter{w: &output}).Write(&types.TCPRetransmitTracing{Comm: "audit\nforged"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `audit\nforged`) {
		t.Fatalf("missing escaped comm: %q", output.String())
	}
	if got := strings.Count(output.String(), "\n"); got != 1 {
		t.Fatalf("one event header has %d lines: %q", got, output.String())
	}
}

func TestJSONWriterPreservesControlBearingComm(t *testing.T) {
	var output bytes.Buffer
	const comm = "line\nbreak\x1b"
	if err := (&jsonWriter{w: &output}).Write(&types.TCPRetransmitTracing{Comm: comm}); err != nil {
		t.Fatal(err)
	}
	var event types.TCPRetransmitTracing
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Comm != comm {
		t.Fatalf("comm=%q", event.Comm)
	}
}
