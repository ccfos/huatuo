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

//go:build !didi

package pcapfilter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/huatuo-ai/go-pcap/filter"
	cbpf "golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

var debug = flag.Bool("debug", false, "dump pcapfilter test program details")

const devlinkTrapReportSection = "raw_tracepoint/devlink_trap_report"

func TestMain(m *testing.M) {
	log.SetLevel("debug")

	if os.Getuid() == 0 {
		if err := bpf.Init(nil); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "initialize BPF resources: %v\n", err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

func dumpPrograms(t *testing.T, spec *ebpf.CollectionSpec, prefix string) {
	if !*debug || spec == nil {
		return
	}

	for name, prog := range spec.Programs {
		t.Logf("========== %s: %s ==========", prefix, name)
		if prog == nil {
			t.Log("nil program")
			continue
		}

		t.Logf("Type: %v", prog.Type)
		t.Logf("Section: %s", prog.SectionName)
		t.Logf("Instructions:\n%s", prog.Instructions)
	}
}

func TestExcludeProgramSections(t *testing.T) {
	t.Parallel()

	spec := &ebpf.CollectionSpec{Programs: map[string]*ebpf.ProgramSpec{
		"software": {SectionName: "tracepoint/skb/kfree_skb"},
		"hardware": {SectionName: devlinkTrapReportSection},
	}}

	excludeProgramSections(spec, []string{devlinkTrapReportSection})

	if _, ok := spec.Programs["hardware"]; ok {
		t.Fatal("hardware program was not excluded")
	}
	if _, ok := spec.Programs["software"]; !ok {
		t.Fatal("software program was excluded")
	}
}

func TestApply(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping: requires root")
	}

	origELF, err := os.ReadFile("../../bpf/net_dropwatch.o")
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	specs, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(origELF))
	if err != nil {
		t.Fatalf("Load spec: %v", err)
	}

	filterExpr := "ip and src net 192.168.1.0/24 and tcp dst port 3306"
	if err := Apply(specs, filterExpr); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	dumpPrograms(t, specs, "Program")

	excludeProgramSections(specs, []string{devlinkTrapReportSection})
	if _, err := bpf.LoadBPFFromCollectionSpec("dropwatch-spec.o", specs, nil); err != nil {
		t.Fatalf("load bpf: %v", err)
	}
}

func TestCompileL2L3L2Predicates(t *testing.T) {
	l2Insts, l3Insts, err := buildL2L3FilterInsts("arp")
	if err != nil {
		t.Fatalf("compile L2/L3 filters: %v", err)
	}

	if len(l2Insts) == 0 {
		t.Fatalf("expected L2 instructions for arp filter")
	}
	if len(l3Insts) == 0 {
		t.Fatalf("expected L3 instructions for arp filter")
	}
}

func TestRawIPFilterPreservesL2PredicateNegation(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = unix.IPPROTO_TCP
	packet[32] = 5 << 4

	tests := []struct {
		expr string
		want bool
	}{
		{expr: "arp"},
		{expr: "rarp"},
		{expr: "not arp", want: true},
		{expr: "not rarp", want: true},
		{expr: "arp or tcp", want: true},
		{expr: "arp and tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			insts, err := compileCBPF(tt.expr, filter.LinkTypeRaw)
			if err != nil {
				t.Fatalf("compileCBPF: %v", err)
			}
			vm, err := cbpf.NewVM(insts)
			if err != nil {
				t.Fatalf("NewVM: %v", err)
			}
			verdict, err := vm.Run(packet)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := verdict != 0; got != tt.want {
				t.Fatalf("filter accepted = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestApplyPatchesEveryProgram(t *testing.T) {
	t.Parallel()

	const originalInstructionCount = 4
	newProgram := func() *ebpf.ProgramSpec {
		return &ebpf.ProgramSpec{
			Instructions: asm.Instructions{
				asm.Mov.Imm(asm.R0, 0).WithSymbol(L3StubSymbol),
				asm.Return(),
				asm.Mov.Imm(asm.R0, 0).WithSymbol(L2StubSymbol),
				asm.Return(),
			},
		}
	}
	spec := &ebpf.CollectionSpec{Programs: map[string]*ebpf.ProgramSpec{
		"retrans_skb":    newProgram(),
		"retrans_synack": newProgram(),
		"retrans_tlp":    newProgram(),
	}}

	l2Insts, l3Insts, err := buildL2L3FilterInsts("tcp and port 443")
	if err != nil {
		t.Fatalf("build filters: %v", err)
	}
	if err := Apply(spec, "tcp and port 443"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	wantInstructions := originalInstructionCount + len(l2Insts) + len(l3Insts)
	for name, prog := range spec.Programs {
		if got := len(prog.Instructions); got != wantInstructions {
			t.Errorf("program %q instruction count = %d, want %d", name, got, wantInstructions)
		}
	}
}

func TestRawIPFilterMatchesSyntheticTCPHeader(t *testing.T) {
	const port = 19997
	insts, err := compileCBPF("tcp and port 19997", filter.LinkTypeRaw)
	if err != nil {
		t.Fatalf("compileCBPF: %v", err)
	}
	vm, err := cbpf.NewVM(insts)
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}

	ipv4 := make([]byte, 40)
	ipv4[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(ipv4[2:4], uint16(len(ipv4)))
	ipv4[8] = 64
	ipv4[9] = unix.IPPROTO_TCP
	binary.BigEndian.PutUint16(ipv4[20:22], port)
	binary.BigEndian.PutUint16(ipv4[22:24], 40000)
	ipv4[32] = 5 << 4
	ipv4[33] = 0x10

	ipv4Options := make([]byte, 44)
	ipv4Options[0] = 4<<4 | 6
	binary.BigEndian.PutUint16(ipv4Options[2:4], uint16(len(ipv4Options)))
	ipv4Options[8] = 64
	ipv4Options[9] = unix.IPPROTO_TCP
	binary.BigEndian.PutUint16(ipv4Options[24:26], 40000)
	binary.BigEndian.PutUint16(ipv4Options[26:28], port)
	ipv4Options[36] = 5 << 4
	ipv4Options[37] = 0x10

	ipv6 := make([]byte, 60)
	ipv6[0] = 6 << 4
	binary.BigEndian.PutUint16(ipv6[4:6], 20)
	ipv6[6] = unix.IPPROTO_TCP
	ipv6[7] = 64
	binary.BigEndian.PutUint16(ipv6[40:42], 40000)
	binary.BigEndian.PutUint16(ipv6[42:44], port)
	ipv6[52] = 5 << 4
	ipv6[53] = 0x10

	wrongPort := append([]byte(nil), ipv4...)
	binary.BigEndian.PutUint16(wrongPort[20:22], 1234)

	tests := []struct {
		name   string
		packet []byte
		want   bool
	}{
		{name: "IPv4", packet: ipv4, want: true},
		{name: "IPv4 options", packet: ipv4Options, want: true},
		{name: "IPv6", packet: ipv6, want: true},
		{name: "wrong port", packet: wrongPort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, err := vm.Run(tt.packet)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := verdict != 0; got != tt.want {
				t.Fatalf("filter accepted = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestRawIPFilterKeepsPortAndEthertypeComparisonsDistinct(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = unix.IPPROTO_TCP
	binary.BigEndian.PutUint16(packet[20:22], 2048)
	binary.BigEndian.PutUint16(packet[22:24], 40000)
	packet[32] = 5 << 4

	insts, err := compileCBPF("tcp and port 2048", filter.LinkTypeRaw)
	if err != nil {
		t.Fatalf("compileCBPF: %v", err)
	}
	vm, err := cbpf.NewVM(insts)
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	verdict, err := vm.Run(packet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verdict == 0 {
		t.Fatal("port 2048 comparison was rewritten as an IPv4 version test")
	}
}

func TestCompileMalformedNetReturnsInvalidFilter(t *testing.T) {
	for _, expr := range []string{
		"udp or net 1.2.3.4/33",
		"udp or net 1.2.3/8",
		"udp or net 1.2.3.4/",
		"port 80 or net 999.0.0.0/8",
	} {
		t.Run(expr, func(t *testing.T) {
			_, _, err := buildL2L3FilterInsts(expr)
			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("buildL2L3FilterInsts(%q) error = %v, want ErrInvalidFilter", expr, err)
			}
		})
	}
}

// makeTCPFilterPacket represents the tuple provided to both filtering consumers.
func makeTCPFilterPacket(version byte, port uint16) []byte {
	headerLength := 20
	if version == 6 {
		headerLength = 40
	}

	packet := make([]byte, headerLength+20)
	if version == 6 {
		packet[0] = 6 << 4
		binary.BigEndian.PutUint16(packet[4:6], 20)
		packet[6] = unix.IPPROTO_TCP
		packet[7] = 64
		copy(packet[8:24], []byte{0x20, 1, 0x0d, 0xb8})
		copy(packet[24:40], packet[8:24])
		packet[23] = 1
		packet[39] = 2
	} else {
		packet[0] = 4<<4 | 5
		binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
		packet[8] = 64
		packet[9] = unix.IPPROTO_TCP
		copy(packet[12:16], []byte{10, 0, 0, 1})
		copy(packet[16:20], []byte{10, 0, 0, 2})
	}

	binary.BigEndian.PutUint16(packet[headerLength:headerLength+2], 12345)
	binary.BigEndian.PutUint16(packet[headerLength+2:headerLength+4], port)
	packet[headerLength+12] = 5 << 4

	return packet
}

func TestCompileCBPFBooleanVerdicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		expr string
		port uint16
		want bool
	}{
		{
			name: "qualified host accepts both ip versions",
			expr: "tcp src host 10.0.0.1 or tcp src host 2001:db8::1",
			port: 80,
			want: true,
		},
		{
			name: "qualified host excludes another address",
			expr: "tcp src host 192.0.2.1 or tcp src host 2001:db9::1",
			port: 80,
		},
		{
			name: "qualified network accepts both ip versions",
			expr: "tcp src net 10.0.0.0/24 or tcp src net 2001:db8::/64",
			port: 80,
			want: true,
		},
		{
			name: "qualified network excludes another prefix",
			expr: "tcp src net 192.0.2.0/24 or tcp src net 2001:db9::/64",
			port: 80,
		},
		{
			name: "negated group accepts another port",
			expr: "not (port 443 or port 22)",
			port: 80,
			want: true,
		},
		{
			name: "negated group rejects matching port",
			expr: "not (port 443 or port 22)",
			port: 443,
		},
		{
			name: "grouped port alternatives accept",
			expr: "tcp and (port 80 or port 443)",
			port: 443,
			want: true,
		},
		{
			name: "grouped port alternatives reject",
			expr: "tcp and (port 80 or port 443)",
			port: 2048,
		},
		{
			name: "l2 predicate mixed with tcp",
			expr: "arp or tcp",
			port: 80,
			want: true,
		},
		{
			name: "negated l2 predicate",
			expr: "not arp and tcp",
			port: 80,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, version := range []byte{4, 6} {
				for _, linkType := range []filter.LinkType{filter.LinkTypeEthernet, filter.LinkTypeRaw} {
					packet := makeTCPFilterPacket(version, tt.port)
					if linkType == filter.LinkTypeEthernet {
						frame := make([]byte, ethernetHeaderLen)
						etherType := uint16(0x0800)
						if version == 6 {
							etherType = 0x86dd
						}
						binary.BigEndian.PutUint16(frame[12:14], etherType)
						packet = append(frame, packet...)
					}

					instructions, err := compileCBPF(tt.expr, linkType)
					if err != nil {
						t.Fatal(err)
					}

					vm, err := cbpf.NewVM(instructions)
					if err != nil {
						t.Fatal(err)
					}

					got, err := vm.Run(packet)
					if err != nil || (got != 0) != tt.want {
						t.Fatalf("IPv%d link %v verdict=%d error=%v, want accept=%t", version, linkType, got, err, tt.want)
					}

					if _, err := buildFilterInsts(tt.expr, "_test", linkType); err != nil {
						t.Fatalf("translate IPv%d link %v: %v", version, linkType, err)
					}
				}
			}
		})
	}
}

func TestValidateL3CompatiblePacketFields(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		"ether host 02:00:00:00:00:01",
		"tcp[13] & 2 != 0", "ip [ 8 ] = 64", "TCP[13] = 2",
		"tcp and (ip[8] = 64 or udp)",
		"len > 40", "LEN >= 60", "tcp and (len > 40 or udp)",
	} {
		t.Run(expression, func(t *testing.T) {
			if err := ValidateL3Compatible(expression); !errors.Is(err, ErrL3IncompatibleFilter) {
				t.Fatalf("ValidateL3Compatible(%q)=%v, want ErrL3IncompatibleFilter", expression, err)
			}
		})
	}

	for _, expression := range []string{"ether proto ip and tcp", "ether proto ip6 and tcp", "not arp and tcp", "tcp and (port 80 or port 443)"} {
		t.Run(expression, func(t *testing.T) {
			if err := ValidateL3Compatible(expression); err != nil {
				t.Fatalf("ValidateL3Compatible(%q): %v", expression, err)
			}
		})
	}
}

func TestBuildFilterRejectsPacketLength(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{"len > 40", "tcp[13] = 2", "ip[8] = 64"} {
		t.Run(expression, func(t *testing.T) {
			for _, linkType := range []filter.LinkType{filter.LinkTypeEthernet, filter.LinkTypeRaw} {
				if _, err := buildFilterInsts(expression, "_test", linkType); !errors.Is(err, ErrInvalidFilter) {
					t.Fatalf("buildFilterInsts(%q, %v)=%v, want ErrInvalidFilter", expression, linkType, err)
				}
			}
		})
	}
}

func TestCompileCBPFGroupedARP(t *testing.T) {
	t.Parallel()

	packet := make([]byte, ethernetHeaderLen+28)
	binary.BigEndian.PutUint16(packet[12:14], 0x0806)
	instructions, err := compileCBPF("(arp or tcp) and udp", filter.LinkTypeEthernet)
	if err != nil {
		t.Fatal(err)
	}

	vm, err := cbpf.NewVM(instructions)
	if err != nil {
		t.Fatal(err)
	}

	verdict, err := vm.Run(packet)
	if err != nil || verdict != 0 {
		t.Fatalf("(arp or tcp) and udp on ARP verdict=%d error=%v, want reject", verdict, err)
	}
}

func TestCompileCBPFIPv6Port(t *testing.T) {
	t.Parallel()

	instructions, err := compileCBPF("ip6 and tcp port 80", filter.LinkTypeRaw)
	if err != nil {
		t.Fatal(err)
	}

	vm, err := cbpf.NewVM(instructions)
	if err != nil {
		t.Fatal(err)
	}

	for _, version := range []byte{4, 6} {
		verdict, err := vm.Run(makeTCPFilterPacket(version, 80))
		if err != nil || (verdict != 0) != (version == 6) {
			t.Fatalf("IPv%d verdict=%d error=%v, want accept=%t", version, verdict, err, version == 6)
		}
	}
}

func TestCompileCBPFGroupedHostAndPort(t *testing.T) {
	t.Parallel()

	instructions, err := compileCBPF("(src host 10.0.0.1 and dst port 80) or dst port 443", filter.LinkTypeRaw)
	if err != nil {
		t.Fatal(err)
	}

	vm, err := cbpf.NewVM(instructions)
	if err != nil {
		t.Fatal(err)
	}

	for _, port := range []uint16{80, 443, 2048} {
		verdict, err := vm.Run(makeTCPFilterPacket(4, port))
		if err != nil || (verdict != 0) != (port != 2048) {
			t.Fatalf("port %d verdict=%d error=%v, want accept=%t", port, verdict, err, port != 2048)
		}
	}
}
