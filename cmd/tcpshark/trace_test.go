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

import "testing"

func TestRetransmitAttachOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		isTLPEnabled     bool
		expectedPrograms []string
		expectedSymbols  []string
	}{
		{
			name:             "tlp disabled by default",
			expectedPrograms: []string{bpfProgramTCPRetransmitSKB, bpfProgramTCPRetransmitSynack},
			expectedSymbols:  []string{"tcp/tcp_retransmit_skb", "tcp/tcp_retransmit_synack"},
		},
		{
			name:         "tlp enabled",
			isTLPEnabled: true,
			expectedPrograms: []string{
				bpfProgramTCPRetransmitSKB,
				bpfProgramTCPRetransmitSynack,
				bpfProgramTCPRetransmitTLP,
			},
			expectedSymbols: []string{
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
			if len(options) != len(tt.expectedPrograms) {
				t.Fatalf("attach option count = %d, want %d", len(options), len(tt.expectedPrograms))
			}
			for i, expectedProgram := range tt.expectedPrograms {
				if options[i].ProgramName != expectedProgram {
					t.Errorf("option %d program = %q, want %q", i, options[i].ProgramName, expectedProgram)
				}
				if options[i].Symbol != tt.expectedSymbols[i] {
					t.Errorf("option %d symbol = %q, want %q", i, options[i].Symbol, tt.expectedSymbols[i])
				}
			}
		})
	}
}
