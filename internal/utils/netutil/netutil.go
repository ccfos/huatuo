// Copyright 2025 The HuaTuo Authors
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
	"math/bits"
	"net"

	"github.com/vishvananda/netlink/nl"
)

var NativeEndian = nl.NativeEndian()

// Inetv4Ntop, inet_ntop
// convert IPv4 addresses (in network byte order) from binary to text.
//
// https://man7.org/linux/man-pages/man3/inet_ntop.3.html
func Inetv4Ntop(addr uint32) net.IP {
	return net.IPv4(
		byte(addr>>24),
		byte(addr>>16),
		byte(addr>>8),
		byte(addr),
	).To4()
}

// Ntohs
// converts the unsigned short integer netshort from network byte order to host byte order.
//
// https://linux.die.net/man/3/ntohs
func Ntohs(val uint16) uint16 {
	if NativeEndian == binary.LittleEndian {
		return (val >> 8) | (val << 8)
	}
	return val
}

// Ntohl
// converts the unsigned integer netlong from network byte order to host byte order.
//
// https://linux.die.net/man/3/ntohs
func Ntohl(val uint32) uint32 {
	if NativeEndian == binary.LittleEndian {
		return bits.ReverseBytes32(val)
	}

	return val
}

// Htons
// converts the unsigned short integer hostshort from host byte order to network byte order.
func Htons(val uint16) uint16 {
	if NativeEndian == binary.LittleEndian {
		return (val >> 8) | (val << 8)
	}
	return val
}

// Htonl
// converts the unsigned integer hostlong from host byte order to network byte order.
func Htonl(val uint32) uint32 {
	if NativeEndian == binary.LittleEndian {
		return bits.ReverseBytes32(val)
	}
	return val
}
