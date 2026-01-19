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
	"encoding/binary"
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
			ip:   0x7f000001, // 127.0.0.1 in host byte order (little-endian assumption)
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

func TestNtohs(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
		want uint16
	}{
		{
			name: "Basic",
			val:  0x1234, // the network order
			want: func() uint16 {
				if NativeEndian == binary.LittleEndian {
					return 0x3412
				}
				return 0x1234
			}(),
		},
		{
			name: "Zero",
			val:  0x0000,
			want: 0x0000,
		},
		{
			name: "Max",
			val:  0xffff,
			want: 0xffff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Ntohs(tt.val); got != tt.want {
				t.Errorf("InetNtohs() = %x, want %x", got, tt.want)
			}
		})
	}
}

func TestNtohl(t *testing.T) {
	tests := []struct {
		name string
		val  uint32
		want uint32
	}{
		{
			name: "Basic",
			val:  0x12345678, // network order
			want: func() uint32 {
				if NativeEndian == binary.LittleEndian {
					return 0x78563412
				}
				return 0x12345678
			}(),
		},
		{
			name: "Zero",
			val:  0x00000000,
			want: 0x00000000,
		},
		{
			name: "Max",
			val:  0xffffffff,
			want: 0xffffffff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Ntohl(tt.val); got != tt.want {
				t.Errorf("InetNtohl() = %x, want %x", got, tt.want)
			}
		})
	}
}

func TestHtons(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
		want uint16
	}{
		{
			name: "Basic",
			val:  0x1234, // host order
			want: func() uint16 {
				if NativeEndian == binary.LittleEndian {
					return 0x3412
				}
				return 0x1234
			}(),
		},
		{
			name: "Zero",
			val:  0x0000,
			want: 0x0000,
		},
		{
			name: "Max",
			val:  0xffff,
			want: 0xffff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Htons(tt.val); got != tt.want {
				t.Errorf("InetHtons() = %x, want %x", got, tt.want)
			}
		})
	}
}

func TestHtonl(t *testing.T) {
	tests := []struct {
		name string
		val  uint32
		want uint32
	}{
		{
			name: "Basic",
			val:  0x12345678, // host order
			want: func() uint32 {
				if NativeEndian == binary.LittleEndian {
					return 0x78563412
				}
				return 0x12345678
			}(),
		},
		{
			name: "Zero",
			val:  0x00000000,
			want: 0x00000000,
		},
		{
			name: "Max",
			val:  0xffffffff,
			want: 0xffffffff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Htonl(tt.val); got != tt.want {
				t.Errorf("InetHtonl() = %x, want %x", got, tt.want)
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
