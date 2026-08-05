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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"huatuo-bamai/internal/bpf"
)

func TestLoadRetransmitBPFReturnsReadError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.o")
	_, err := loadRetransmitBPF(
		path,
		"",
		bpf.NewRateLimiter("tcp_retransmit", 0),
	)
	if err == nil || !strings.Contains(err.Error(), "read bpf") {
		t.Fatalf("loadRetransmitBPF() error = %v, want read bpf error", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("loadRetransmitBPF() error = %v, want *os.PathError", err)
	}
}

func TestRetransmitAttachOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		isTLPEnabled bool
		wantPrograms []string
		wantSymbols  []string
	}{
		{
			name:         "tlp disabled by default",
			wantPrograms: []string{"retrans_skb", "retrans_synack"},
			wantSymbols:  []string{"tcp/tcp_retransmit_skb", "tcp/tcp_retransmit_synack"},
		},
		{
			name:         "tlp enabled",
			isTLPEnabled: true,
			wantPrograms: []string{
				"retrans_skb",
				"retrans_synack",
				"retrans_tlp",
			},
			wantSymbols: []string{
				"tcp/tcp_retransmit_skb",
				"tcp/tcp_retransmit_synack",
				"tcp_send_loss_probe",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := retransmitAttachOptions(tt.isTLPEnabled)
			if len(options) != len(tt.wantPrograms) {
				t.Fatalf("attach option count = %d, want %d", len(options), len(tt.wantPrograms))
			}
			for i, wantProgram := range tt.wantPrograms {
				if options[i].ProgramName != wantProgram {
					t.Errorf("option %d program = %q, want %q", i, options[i].ProgramName, wantProgram)
				}
				if options[i].Symbol != tt.wantSymbols[i] {
					t.Errorf("option %d symbol = %q, want %q", i, options[i].Symbol, tt.wantSymbols[i])
				}
			}
		})
	}
}
