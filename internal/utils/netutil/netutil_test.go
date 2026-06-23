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

package netutil

import (
	"net"
	"testing"
)

func TestInetv4Ntop(t *testing.T) {
	tests := []struct {
		name string
		ip   uint32
		want net.IP
	}{
		{
			name: "Loopback IPv4",
			ip:   0x100007f, // 127.0.0.1 in host byte order (little-endian assumption)
			want: net.IPv4(127, 0, 0, 1),
		},
		{
			name: "Zero IP",
			ip:   0x00000000,
			want: net.IPv4(0, 0, 0, 0),
		},
		{
			name: "Broadcast IP",
			ip:   0xffffffff,
			want: net.IPv4(255, 255, 255, 255),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Inetv4Ntop(tt.ip)
			if !got.Equal(tt.want) {
				t.Errorf("InetNtop() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHtonlNtohlSymmetry(t *testing.T) {
	tests := []struct {
		name string
		val  uint32
	}{
		{"Basic", 0x12345678},
		{"Zero", 0x00000000},
		{"Max", 0xffffffff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := Htonl(tt.val)
			host := Ntohl(network)
			if host != tt.val {
				t.Errorf("Ntohl(Htonl(%x)) = %x, want %x", tt.val, host, tt.val)
			}
			// reverse validation
			hostBack := Ntohl(Htonl(host))
			if hostBack != host {
				t.Errorf("Htonl(Ntohl(%x)) = %x, want %x", network, hostBack, network)
			}
		})
	}
}

func TestHtonsNtohsSymmetry(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
	}{
		{"Basic", 0x1234},
		{"Zero", 0x0000},
		{"Max", 0xffff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := Htons(tt.val)
			host := Ntohs(network)
			if host != tt.val {
				t.Errorf("Ntohs(Htons(%x)) = %x, want %x", tt.val, host, tt.val)
			}
			// reverse validation
			hostBack := Ntohs(Htons(host))
			if hostBack != host {
				t.Errorf("Htons(Ntohs(%x)) = %x, want %x", network, hostBack, network)
			}
		})
	}
}

func FuzzInetNtohs(f *testing.F) {
	seeds := []uint16{0x0000, 0x1234, 0xffff}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, val uint16) {
		got := Ntohs(val)
		// Round-trip with htons should return original
		roundTrip := Htons(got)
		if roundTrip != val {
			t.Errorf("InetNtohs(%x) round-trip failed: %x -> %x -> %x", val, val, got, roundTrip)
		}
	})
}

// Performance Tests (Benchmarks)
func BenchmarkInetv4Ntop(b *testing.B) {
	ip := uint32(0x7f000001)
	for i := 0; i < b.N; i++ {
		Inetv4Ntop(ip)
	}
}

func BenchmarkNtohs(b *testing.B) {
	val := uint16(0x1234)
	for i := 0; i < b.N; i++ {
		Ntohs(val)
	}
}

func BenchmarkNtohl(b *testing.B) {
	val := uint32(0x12345678)
	for i := 0; i < b.N; i++ {
		Ntohl(val)
	}
}

func BenchmarkHtons(b *testing.B) {
	val := uint16(0x1234)
	for i := 0; i < b.N; i++ {
		Htons(val)
	}
}

func BenchmarkHtonl(b *testing.B) {
	val := uint32(0x12345678)
	for i := 0; i < b.N; i++ {
		Htonl(val)
	}
}
