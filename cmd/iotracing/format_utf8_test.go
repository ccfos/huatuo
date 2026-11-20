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
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestTextSummaryPreservesUTF8Commands(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want string
	}{
		{name: "short ASCII", comm: "sleep", want: "sleep"},
		{name: "ASCII boundary", comm: "abcdefghijklmnopqrst", want: "abcdefghijklmnopqrst"},
		{name: "long ASCII", comm: "abcdefghijklmnopqrstu", want: "abcdefghijklmnopq..."},
		{name: "short multibyte", comm: "甲乙丙丁戊己庚", want: "甲乙丙丁戊己庚"},
		{name: "multibyte boundary", comm: strings.Repeat("界", 20), want: strings.Repeat("界", 20)},
		{name: "long multibyte", comm: strings.Repeat("界", 21), want: strings.Repeat("界", 17) + "..."},
		{name: "mixed", comm: "abcdefghijklmnop😀qrst", want: "abcdefghijklmnop😀..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			printIOTracingSnapshot(&output, &types.IOTracingSnapshot{
				Processes: []types.ProcessFileIOStats{{PID: 42, Comm: tt.comm}},
			})
			if !utf8.Valid(output.Bytes()) {
				t.Fatalf("report is not valid UTF-8: %q", output.String())
			}
			lines := strings.Split(output.String(), "\n")
			wantRow := fmt.Sprintf("%-7d  %-20s %7s %8s %9s %10s %5d", 42, tt.want, "0B", "0B", "0B", "0B", 0)
			if lines[2] != wantRow {
				t.Fatalf("summary row = %q, want %q", lines[2], wantRow)
			}
			if !strings.Contains(output.String(), fmt.Sprintf("COMMAND: %-20s\n", tt.comm)) {
				t.Fatal("detail row did not retain the complete command")
			}
		})
	}
}
