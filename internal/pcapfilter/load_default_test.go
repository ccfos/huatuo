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
	"flag"
	"os"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	cbpf "golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

var debug = flag.Bool("debug", false, "dump pcapfilter test program details")

const devlinkTrapReportSection = "raw_tracepoint/devlink_trap_report"

func TestMain(m *testing.M) {
	log.SetLevel("debug")

	var rLimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rLimit); err == nil {
		rLimit.Cur = rLimit.Max // raise to kernel maximum
		_ = unix.Setrlimit(unix.RLIMIT_MEMLOCK, &rLimit)
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

	origELF, err := os.ReadFile("../../bpf/dropwatch.o")
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
