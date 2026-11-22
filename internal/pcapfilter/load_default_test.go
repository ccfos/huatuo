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
			insts, err := compileCBPF(tt.expr, true)
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
	insts, err := compileCBPF("tcp and port 19997", true)
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
	binary.BigEndian.PutUint16(packet[20:22], etherTypeIPv4)
	binary.BigEndian.PutUint16(packet[22:24], 40000)
	packet[32] = 5 << 4

	insts, err := compileCBPF("tcp and port 2048", true)
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

func dumpExpr(n *exprNode) string {
	if n == nil {
		return "<nil>"
	}
	switch n.op {
	case exprLeaf:
		return n.text
	case exprNot:
		return "not(" + dumpExpr(n.left) + ")"
	case exprAnd:
		return "and(" + dumpExpr(n.left) + "," + dumpExpr(n.right) + ")"
	case exprOr:
		return "or(" + dumpExpr(n.left) + "," + dumpExpr(n.right) + ")"
	default:
		return "<unknown>"
	}
}

func TestParseFilterExpression(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"tcp", "tcp"},
		{"tcp and port 80", "and(tcp,port 80)"},
		{"tcp or udp", "or(tcp,udp)"},
		{"tcp or udp and port 53", "and(or(tcp,udp),port 53)"},
		{"tcp and port 80 or port 443", "or(and(tcp,port 80),port 443)"},
		{"not tcp", "not(tcp)"},
		{"not not tcp", "not(not(tcp))"},
		{"not (ip or arp)", "not(or(ip,arp))"},
		{"(ip or arp) and (tcp or udp)", "and(or(ip,arp),or(tcp,udp))"},
		{"ip and src net 10.0.0.0/8 and tcp dst port 3306",
			"and(and(ip,src net 10.0.0.0/8),tcp dst port 3306)"},
		{"src and dst host 10.9.9.9", "src and dst host 10.9.9.9"},
		{"src or dst port 80", "src or dst port 80"},
		{"tcp and (port 80 or port 443)", "and(tcp,or(port 80,port 443))"},
		{"not host 10.9.9.9 and port 80", "and(not(host 10.9.9.9),port 80)"},
		{"tcp not port 80", "tcp not port 80"},
		{"src not host 10.0.0.1", "src not host 10.0.0.1"},
		{"host not 10.0.0.1", "host not 10.0.0.1"},
		{"tcp not udp", "tcp not udp"},
		{"tcp and not port 80", "and(tcp,not(port 80))"},
		{"tcp dst port 80 or 443", "or(tcp dst port 80,tcp dst port 443)"},
		{"tcp not port 80 or 443", "or(tcp not port 80,tcp port 443)"},
		{"host 10.0.0.1 or 10.0.0.2", "or(host 10.0.0.1,host 10.0.0.2)"},
		{"port 80 or 443", "or(port 80,port 443)"},
		{"src host 10.0.0.1 or 10.0.0.2", "or(src host 10.0.0.1,src host 10.0.0.2)"},
		{"tcp and (dst port 80 or 443)", "and(tcp,or(dst port 80,dst port 443))"},
		{"ip proto tcp or udp", "or(ip proto tcp,udp)"},
		{"(host 10.0.0.1) or 10.0.0.2", "or(host 10.0.0.1,host 10.0.0.2)"},
		{"(host 10.0.0.1 or host 10.0.0.2) or 10.0.0.3",
			"or(or(host 10.0.0.1,host 10.0.0.2),host 10.0.0.3)"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			node, err := parseFilterExpression(tt.expr)
			if err != nil {
				t.Fatalf("parseFilterExpression: %v", err)
			}
			if got := dumpExpr(node); got != tt.want {
				t.Fatalf("parsed = %q, want %q", got, tt.want)
			}
		})
	}

	errTests := []struct {
		expr string
		want error
	}{
		{"", ErrEmptyFilter},
		{"   ", ErrEmptyFilter},
		{"tcp and", ErrInvalidFilter},
		{"tcp or", ErrInvalidFilter},
		{"not", ErrInvalidFilter},
		{"tcp and not", ErrInvalidFilter},
		{"(tcp or udp", ErrInvalidFilter},
		{"tcp )", ErrInvalidFilter},
		{"()", ErrInvalidFilter},
	}
	for _, tt := range errTests {
		t.Run("err/"+tt.expr, func(t *testing.T) {
			_, err := parseFilterExpression(tt.expr)
			if !errors.Is(err, tt.want) {
				t.Fatalf("parseFilterExpression(%q) error = %v, want %v",
					tt.expr, err, tt.want)
			}
		})
	}
}

func filterTestEthernet(etherType uint16, payload []byte) []byte {
	p := make([]byte, 14+len(payload))
	binary.BigEndian.PutUint16(p[12:14], etherType)
	copy(p[14:], payload)
	return p
}

func filterTestIPv4(proto byte, src, dst [4]byte, sport, dport uint16) []byte {
	p := make([]byte, 24)
	p[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = proto
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	binary.BigEndian.PutUint16(p[20:22], sport)
	binary.BigEndian.PutUint16(p[22:24], dport)
	return p
}

func filterTestIPv6(next byte, src, dst [16]byte, sport, dport uint16) []byte {
	p := make([]byte, 44)
	p[0] = 6 << 4
	p[6] = next
	p[7] = 64
	copy(p[8:24], src[:])
	copy(p[24:40], dst[:])
	binary.BigEndian.PutUint16(p[40:42], sport)
	binary.BigEndian.PutUint16(p[42:44], dport)
	return p
}

func filterTestARP() []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint16(p[0:2], 1)
	binary.BigEndian.PutUint16(p[2:4], 0x0800)
	p[4], p[5] = 6, 4
	binary.BigEndian.PutUint16(p[6:8], 1)
	copy(p[8:14], []byte{0x02, 0, 0, 0, 0, 1})
	copy(p[14:18], []byte{10, 1, 2, 3})
	copy(p[18:24], []byte{0x02, 0, 0, 0, 0, 2})
	copy(p[24:28], []byte{10, 1, 2, 4})
	return p
}

func runFilterProgram(t *testing.T, expr string, l3 bool, pkt []byte) bool {
	t.Helper()

	insts, err := compileCBPF(expr, l3)
	if err != nil {
		t.Fatalf("compileCBPF(%q): %v", expr, err)
	}
	vm, err := cbpf.NewVM(insts)
	if err != nil {
		t.Fatalf("NewVM(%q): %v", expr, err)
	}
	verdict, err := vm.Run(pkt)
	if err != nil {
		t.Fatalf("Run(%q): %v", expr, err)
	}
	return verdict != 0
}

func TestCompoundFilterSemantics(t *testing.T) {
	tcpToHost := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 443))
	tcpToOther := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 1, 1, 1}, 40000, 443))
	udpToHost := filterTestEthernet(0x0800, filterTestIPv4(17,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 443))
	icmpPacket := filterTestEthernet(0x0800, filterTestIPv4(1,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 0, 0))
	tcp3306 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{192, 168, 1, 5}, [4]byte{10, 9, 9, 9}, 40000, 3306))
	udp3306 := filterTestEthernet(0x0800, filterTestIPv4(17,
		[4]byte{192, 168, 1, 5}, [4]byte{10, 9, 9, 9}, 40000, 3306))
	tcpWrongSrc := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 0, 0, 1}, [4]byte{10, 9, 9, 9}, 40000, 3306))
	tcpWrongPort := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{192, 168, 1, 5}, [4]byte{10, 9, 9, 9}, 40000, 80))
	tcp53 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 53))
	tcp80 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 80))
	tcpOther80 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 1, 1, 1}, 40000, 80))
	tcp8080 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 8080))
	tcpToHost1 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 0, 0, 1}, 40000, 443))
	tcpToHost2 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 0, 0, 2}, 40000, 443))
	tcpSrcHost1 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 0, 0, 1}, [4]byte{10, 9, 9, 9}, 40000, 443))
	tcpSrcHost2 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 0, 0, 2}, [4]byte{10, 9, 9, 9}, 40000, 443))
	udp80 := filterTestEthernet(0x0800, filterTestIPv4(17,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 80))
	udp53 := filterTestEthernet(0x0800, filterTestIPv4(17,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 53))
	tcpSameSrcDst := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 9, 9, 9}, [4]byte{10, 9, 9, 9}, 40000, 443))
	arpPacket := filterTestEthernet(0x0806, filterTestARP())
	rarpPacket := filterTestEthernet(0x8035, filterTestARP())
	ipv6Packet := filterTestEthernet(0x86dd, filterTestIPv6(6,
		[16]byte{0xfe, 0x80}, [16]byte{0xfe, 0x80, 0, 1}, 40000, 443))

	cases := []struct {
		name string
		expr string
		l3   bool
		pkt  []byte
		want bool
	}{
		{"tcp-and-host/tcp", "tcp and host 10.9.9.9", false, tcpToHost, true},
		{"tcp-and-host/udp", "tcp and host 10.9.9.9", false, udpToHost, false},
		{"tcp-and-host/other", "tcp and host 10.9.9.9", false, tcpToOther, false},
		{"ip-or-arp/ipv4", "ip or arp", false, tcpToHost, true},
		{"ip-or-arp/arp", "ip or arp", false, arpPacket, true},
		{"ip-or-arp/ipv6", "ip or arp", false, ipv6Packet, false},
		{"tcp-and-not-host/other", "tcp and not host 10.9.9.9", false, tcpToOther, true},
		{"tcp-and-not-host/host", "tcp and not host 10.9.9.9", false, tcpToHost, false},
		{"tcp-and-not-host/udp", "tcp and not host 10.9.9.9", false, udpToHost, false},
		{"not-not-tcp/tcp", "not not tcp", false, tcpToHost, true},
		{"not-not-tcp/udp", "not not tcp", false, udpToHost, false},
		{"tcp-or-arp-or-rarp/tcp", "tcp or (arp or rarp)", false, tcpToHost, true},
		{"tcp-or-arp-or-rarp/arp", "tcp or (arp or rarp)", false, arpPacket, true},
		{"tcp-or-arp-or-rarp/rarp", "tcp or (arp or rarp)", false, rarpPacket, true},
		{"not-ip-or-arp/ipv6", "not (ip or arp)", false, ipv6Packet, true},
		{"not-ip-or-arp/ipv4", "not (ip or arp)", false, tcpToHost, false},
		{"not-ip-or-arp/arp", "not (ip or arp)", false, arpPacket, false},
		{"ip-arp-and-tcp-udp/tcp", "(ip or arp) and (tcp or udp)", false, tcpToHost, true},
		{"ip-arp-and-tcp-udp/udp", "(ip or arp) and (tcp or udp)", false, udpToHost, true},
		{"ip-arp-and-tcp-udp/ipv6", "(ip or arp) and (tcp or udp)", false, ipv6Packet, false},
		{"ip-arp-and-tcp-udp/arp", "(ip or arp) and (tcp or udp)", false, arpPacket, false},
		{"tcp-port-or-ports/443", "tcp and (port 80 or port 443)", false, tcpToHost, true},
		{"tcp-port-or-ports/80", "tcp and (port 80 or port 443)", false, tcp80, true},
		{"tcp-port-or-ports/udp", "tcp and (port 80 or port 443)", false, udpToHost, false},
		{"net-and-port/match", "ip and src net 192.168.1.0/24 and tcp dst port 3306", false, tcp3306, true},
		{"net-and-port/udp", "ip and src net 192.168.1.0/24 and tcp dst port 3306", false, udp3306, false},
		{"net-and-port/wrong-src", "ip and src net 192.168.1.0/24 and tcp dst port 3306", false, tcpWrongSrc, false},
		{"net-and-port/wrong-port", "ip and src net 192.168.1.0/24 and tcp dst port 3306", false, tcpWrongPort, false},
		{"left-assoc/tcp53", "tcp or udp and port 53", false, tcp53, true},
		{"left-assoc/tcp80", "tcp or udp and port 53", false, tcp80, false},
		{"left-assoc/udp53", "tcp or udp and port 53", false, udp53, true},
		{"left-assoc/icmp", "tcp or udp and port 53", false, icmpPacket, false},
		{"src-and-dst/match", "src and dst host 10.9.9.9", false, tcpSameSrcDst, true},
		{"src-and-dst/mismatch", "src and dst host 10.9.9.9", false, tcpToHost, false},
		{"not-tcp-or-udp/icmp", "not (tcp or udp)", false, icmpPacket, true},
		{"not-tcp-or-udp/tcp", "not (tcp or udp)", false, tcpToHost, false},
		{"not-ports/80", "tcp and not (port 80 or port 443)", false, tcpWrongPort, false},
		{"not-ports/3306", "tcp and not (port 80 or port 443)", false, tcp3306, true},
		{"not-host-and-port/other", "not host 10.9.9.9 and port 80", false, tcpOther80, true},
		{"not-host-and-port/host", "not host 10.9.9.9 and port 80", false, tcp80, false},
		{"not-host-and-port/wrong-port", "not host 10.9.9.9 and port 80", false, tcpToHost, false},
		{"not-ports-and-tcp/3306", "not (port 80 or port 443) and tcp", false, tcp3306, true},
		{"not-ports-and-tcp/80", "not (port 80 or port 443) and tcp", false, tcp80, false},
		{"not-ports-and-tcp/443", "not (port 80 or port 443) and tcp", false, tcpToHost, false},
		{"shorthand-port/80", "tcp dst port 80 or 443", false, tcp80, true},
		{"shorthand-port/443", "tcp dst port 80 or 443", false, tcpToHost, true},
		{"shorthand-port/8080", "tcp dst port 80 or 443", false, tcp8080, false},
		{"shorthand-port/udp", "tcp dst port 80 or 443", false, udpToHost, false},
		{"shorthand-host/a", "host 10.0.0.1 or 10.0.0.2", false, tcpToHost1, true},
		{"shorthand-host/b", "host 10.0.0.1 or 10.0.0.2", false, tcpToHost2, true},
		{"shorthand-host/other", "host 10.0.0.1 or 10.0.0.2", false, tcpToHost, false},
		{"shorthand-group-port/80", "tcp and (dst port 80 or 443)", false, tcp80, true},
		{"shorthand-group-port/443", "tcp and (dst port 80 or 443)", false, tcpToHost, true},
		{"shorthand-group-port/8080", "tcp and (dst port 80 or 443)", false, tcp8080, false},
		{"shorthand-group-port/udp", "tcp and (dst port 80 or 443)", false, udpToHost, false},
		{"shorthand-port-only/443", "port 80 or 443", false, udpToHost, true},
		{"shorthand-src-host/a", "src host 10.0.0.1 or 10.0.0.2", false, tcpSrcHost1, true},
		{"shorthand-src-host/b", "src host 10.0.0.1 or 10.0.0.2", false, tcpSrcHost2, true},
		{"shorthand-src-host/other", "src host 10.0.0.1 or 10.0.0.2", false, tcpToHost, false},
		{"shorthand-inner-not/80", "tcp not port 80 or 443", false, tcp80, false},
		{"shorthand-inner-not/443", "tcp not port 80 or 443", false, tcpToHost, true},
		{"shorthand-inner-not/8080", "tcp not port 80 or 443", false, tcp8080, true},
		{"shorthand-inner-not/udp80", "tcp not port 80 or 443", false, udp80, true},
		{"inner-not/tcp-80", "tcp not port 80", false, tcp80, false},
		{"inner-not/tcp-443", "tcp not port 80", false, tcpToHost, true},
		{"inner-not/udp-80", "tcp not port 80", false, udp80, true},
		{"inner-not/src-host/src", "src not host 10.0.0.1", false, tcpSrcHost1, false},
		{"inner-not/src-host/other", "src not host 10.0.0.1", false, tcpToHost, true},
		{"inner-not/src-host/dst", "src not host 10.0.0.1", false, tcpToHost1, true},
		{"l3/tcp-and-host/tcp", "tcp and host 10.9.9.9", true, tcpToHost[14:], true},
		{"l3/tcp-and-host/udp", "tcp and host 10.9.9.9", true, udpToHost[14:], false},
		{"l3/ip-or-arp/ipv4", "ip or arp", true, tcpToHost[14:], true},
		{"l3/ip-or-arp/ipv6", "ip or arp", true, ipv6Packet[14:], false},
		{"l3/not-not-tcp/tcp", "not not tcp", true, tcpToHost[14:], true},
		{"l3/not-not-tcp/udp", "not not tcp", true, udpToHost[14:], false},
		{"l3/not-ip-or-arp/ipv6", "not (ip or arp)", true, ipv6Packet[14:], true},
		{"l3/not-ip-or-arp/ipv4", "not (ip or arp)", true, tcpToHost[14:], false},
		{"l3/tcp-port-or-ports/443", "tcp and (port 80 or port 443)", true, tcpToHost[14:], true},
		{"l3/tcp-port-or-ports/udp", "tcp and (port 80 or port 443)", true, udpToHost[14:], false},
		{"l3/tcp-or-arp-or-rarp/tcp", "tcp or (arp or rarp)", true, tcpToHost[14:], true},
		{"l3/shorthand-port/443", "tcp dst port 80 or 443", true, tcpToHost[14:], true},
		{"l3/shorthand-port/8080", "tcp dst port 80 or 443", true, tcp8080[14:], false},
		{"l3/shorthand-host/a", "host 10.0.0.1 or 10.0.0.2", true, tcpToHost1[14:], true},
		{"l3/inner-not/tcp-80", "tcp not port 80", true, tcp80[14:], false},
		{"l3/inner-not/tcp-443", "tcp not port 80", true, tcpToHost[14:], true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runFilterProgram(t, tc.expr, tc.l3, tc.pkt); got != tc.want {
				t.Fatalf("filter accepted = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCompoundFilterErrors(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want error
	}{
		{"empty", "", ErrEmptyFilter},
		{"blank", "   ", ErrEmptyFilter},
		{"dangling-and", "tcp and", ErrInvalidFilter},
		{"unclosed", "(tcp and port 80", ErrInvalidFilter},
		{"empty-group", "()", ErrInvalidFilter},
		{"bad-net", "udp or net 1.2.3.4/33", ErrInvalidFilter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileCBPF(tt.expr, false)
			if !errors.Is(err, tt.want) {
				t.Fatalf("compileCBPF(%q) error = %v, want %v", tt.expr, err, tt.want)
			}
		})
	}
}

func TestCompileLeafAcceptsNegatedLayout(t *testing.T) {
	program, err := compileLeaf("src not host 10.0.0.1")
	if err != nil {
		t.Fatalf("compileLeaf(negated): %v", err)
	}
	if program.keepIdx <= program.dropIdx {
		t.Fatalf("negated leaf keepIdx=%d dropIdx=%d, want keep after drop",
			program.keepIdx, program.dropIdx)
	}
}

func TestCompoundFilterAcceptsLegacyPrimitiveSyntax(t *testing.T) {
	exprs := []string{
		"tcp dst port 80 or 443",
		"tcp dst port 80 or 443 or 8080",
		"host 10.0.0.1 or 10.0.0.2",
		"tcp and (dst port 80 or 443)",
		"port 80 or 443",
		"net 10.0.0.0/8 or 192.168.0.0/16",
		"src host 10.0.0.1 or 10.0.0.2",
		"src and dst host 10.0.0.1 or 10.0.0.2",
		"tcp port 80 or 443",
		"tcp or 443",
		"tcp not udp",
		"tcp not port 80",
		"tcp not port 80 or 443",
		"src not host 10.0.0.1",
		"host not 10.0.0.1",
		"host not 10.0.0.1 or 10.0.0.2",
		"ip proto tcp or udp",
		"(host 10.0.0.1) or 10.0.0.2",
		"(host 10.0.0.1 or host 10.0.0.2) or 10.0.0.3",
	}
	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			if _, err := compileCBPF(expr, false); err != nil {
				t.Fatalf("compileCBPF(%q): %v", expr, err)
			}
		})
	}
}

func TestCompoundFilterLongChain(t *testing.T) {
	expr := "tcp"
	for port := 80; port < 100; port++ {
		expr += fmt.Sprintf(" and not port %d", port)
	}

	tcp443 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 443))
	tcp80 := filterTestEthernet(0x0800, filterTestIPv4(6,
		[4]byte{10, 1, 2, 3}, [4]byte{10, 9, 9, 9}, 40000, 80))

	if !runFilterProgram(t, expr, false, tcp443) {
		t.Fatal("long AND chain rejected matching TCP packet")
	}
	if runFilterProgram(t, expr, false, tcp80) {
		t.Fatal("long AND chain accepted excluded port")
	}
}
