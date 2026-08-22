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

package hccn

import "testing"

func TestParseLinkStatus(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{name: "up", output: "link status: UP\n", want: "UP"},
		{name: "down with extra whitespace", output: " link  status:  DOWN \n", want: "DOWN"},
		{name: "unsupported status", output: "link status: DEGRADED\n", wantErr: true},
		{name: "unavailable status", output: "link status: Unknown!\n", wantErr: true},
		{name: "unexpected shape", output: "status: UP\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLinkStatus(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLinkStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseLinkStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseInterfaceTraffic(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantTX  float64
		wantRX  float64
		wantErr bool
	}{
		{
			name:   "complete output",
			output: "Bandwidth TX: 12.50 MB/sec\nBandwidth RX: 8.25 MB/sec\n",
			wantTX: 12.5,
			wantRX: 8.25,
		},
		{
			name:    "missing RX",
			output:  "Bandwidth TX: 12.50 MB/sec\n",
			wantErr: true,
		},
		{
			name:    "invalid TX value",
			output:  "Bandwidth TX: unavailable MB/sec\nBandwidth RX: 8.25 MB/sec\n",
			wantErr: true,
		},
		{
			name:    "negative TX value",
			output:  "Bandwidth TX: -1 MB/sec\nBandwidth RX: 8.25 MB/sec\n",
			wantErr: true,
		},
		{
			name:    "NaN RX value",
			output:  "Bandwidth TX: 12.50 MB/sec\nBandwidth RX: NaN MB/sec\n",
			wantErr: true,
		},
		{
			name:    "infinite RX value",
			output:  "Bandwidth TX: 12.50 MB/sec\nBandwidth RX: +Inf MB/sec\n",
			wantErr: true,
		},
		{
			name:    "duplicate direction",
			output:  "Bandwidth TX: 12.50 MB/sec\nBandwidth TX: 9.00 MB/sec\nBandwidth RX: 8.25 MB/sec\n",
			wantErr: true,
		},
		{
			name:    "unexpected unit",
			output:  "Bandwidth TX: 12.50 GB/sec\nBandwidth RX: 8.25 MB/sec\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, rx, err := parseInterfaceTraffic(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseInterfaceTraffic() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tx != tt.wantTX || rx != tt.wantRX {
				t.Errorf("parseInterfaceTraffic() = (%v, %v), want (%v, %v)", tx, rx, tt.wantTX, tt.wantRX)
			}
		})
	}
}
