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

// Package pcapfilter compiles tcpdump filter expressions and injects them into
// pre-compiled BPF CollectionSpecs using the pwru stub-splice pattern.
//
// Usage:
//
//	spec, _ := ebpf.LoadCollectionSpec("prog.o")
//	if err := pcapfilter.Apply(spec, "host 10.0.0.1"); err != nil { ... }
//	coll, _ := ebpf.NewCollection(spec)
//
// Limitations inherited from go-pcap's pure-Go compiler:
//   - No byte-offset expressions (e.g. tcp[tcpflags], ip[8]).
//   - Primitives limited to host/port/net/proto on ip/ip6/ether/arp/tcp/udp/stp.
//   - ether host <mac> on L3 produces undefined matches (no MAC header).
package pcapfilter

import (
	"errors"
	"fmt"

	"github.com/cilium/ebpf/asm"
	"github.com/cloudflare/cbpfc"
	"github.com/packetcap/go-pcap/filter"
	cbpf "golang.org/x/net/bpf"
)

var (
	// ErrEmptyFilter is returned when an empty string is passed as the filter expression.
	ErrEmptyFilter = errors.New("empty filter expression")
	// ErrInvalidFilter is returned when the filter expression cannot be parsed.
	ErrInvalidFilter = errors.New("invalid filter expression")
	// ErrStubNotFound is returned when the expected stub symbol is absent from the BPF object.
	ErrStubNotFound = errors.New("stub symbol not found")

	// ErrL2OnlyFilter is returned when the filter matches an L2-only protocol (e.g. arp)
	// with no L3 equivalent; buildL2L3FilterInsts substitutes a reject-all L3 program.
	ErrL2OnlyFilter = errors.New("matches L2-only protocol")
)

const (
	// ethernetHeaderLen is the L2 Ethernet header length assumed by go-pcap's
	// filter compiler. We subtract it from packet offsets to retarget filters
	// at L3 (DLT_RAW) packets.
	ethernetHeaderLen = 14

	etherTypeIPv4 = 0x0800
	etherTypeIPv6 = 0x86dd
)

// L2StubSymbol and L3StubSymbol are the __noinline BPF function names that mark
// the injection points. The C program must declare both pcap_stub_l2 and pcap_stub_l3.
const (
	L2StubSymbol = "pcap_stub_l2"
	L3StubSymbol = "pcap_stub_l3"
)

// stack slot offsets used by patchPacketLoads.
// R4/R5 hold packet data/data_end on entry; cbpfc scratch starts at +cbpfcStackOffset.
// These slots must not overlap cbpfc's StackOffset region.
const (
	bpfReadKernelSlot int16 = -8
	saveR1Slot        int16 = -16
	saveR2Slot        int16 = -24
	saveR3Slot        int16 = -32
	saveR4Slot        int16 = -40
	saveR5Slot        int16 = -48
	cbpfcStackOffset        = 56
)

func buildL2L3FilterInsts(filterExpr string) (asm.Instructions, asm.Instructions, error) {
	l2Insts, err := buildFilterInsts(filterExpr, "_l2", false)
	if err != nil {
		return nil, nil, err
	}

	l3Insts, err := buildFilterInsts(filterExpr, "_l3", true)
	if err != nil {
		if !errors.Is(err, ErrL2OnlyFilter) {
			return nil, nil, err
		}
		// L2-only filter (e.g. arp): no L3 equivalent, reject all packets on the L3 stub.
		// R4 = R5 (data == data_end) makes the stub pass-through evaluate to 0.
		l3Insts = asm.Instructions{asm.Mov.Reg(asm.R4, asm.R5)}
	}

	return l2Insts, l3Insts, nil
}

func buildFilterInsts(expr, suffix string, l3 bool) (asm.Instructions, error) {
	cbpfInsns, err := compileCBPF(expr, l3)
	if err != nil {
		return nil, fmt.Errorf("compile cBPF: %w", err)
	}

	opts := cbpfc.EBPFOpts{
		PacketStart: asm.R4,
		PacketEnd:   asm.R5,
		Result:      asm.R0,
		ResultLabel: "pcapfilter_result" + suffix,
		Working:     [4]asm.Register{asm.R0, asm.R1, asm.R2, asm.R3},
		LabelPrefix: "pcapfilter" + suffix,
		StackOffset: cbpfcStackOffset,
	}

	ebpfInsns, err := cbpfc.ToEBPF(cbpfInsns, opts)
	if err != nil {
		return nil, fmt.Errorf("translate cBPF->eBPF: %w", err)
	}

	return patchPacketLoads(ebpfInsns, opts.ResultLabel)
}

// patchPacketLoads replaces direct packet-memory loads produced by cbpfc.ToEBPF with
// bpf_probe_read_kernel sequences (required in kprobe/tracepoint context where
// the verifier rejects scalar-pointer dereferences), then appends the epilogue
// that cbpfc's ResultLabel jump lands on.
//
// Register contract on entry to StubSymbol: R4=data (L2 header), R5=data_end.
// Epilogue sets R1=R2=R3=0, R4=verdict(R0), R5=0 so the stub's pass-through
// body evaluates to (verdict != 0).
//
// This mirrors pwru's adjustEBPF in internal/libpcap/compile.go, adapted for
// tracing programs where packet data is passed as a regular pointer register.
func patchPacketLoads(insts asm.Instructions, resultLabel string) (asm.Instructions, error) {
	type patch struct {
		idx  int
		repl asm.Instructions
	}
	var patches []patch

	for idx, inst := range insts {
		// Skip non-loads and stack loads (cbpfc M[] scratch uses RFP as src;
		// only packet loads from PacketStart need bpf_probe_read_kernel).
		if !inst.OpCode.Class().IsLoad() || inst.Src == asm.RFP {
			continue
		}
		repl := asm.Instructions{
			asm.StoreMem(asm.RFP, saveR1Slot, asm.R1, asm.DWord),
			asm.StoreMem(asm.RFP, saveR2Slot, asm.R2, asm.DWord),
			asm.StoreMem(asm.RFP, saveR3Slot, asm.R3, asm.DWord),
			asm.Mov.Reg(asm.R1, asm.RFP),
			asm.Add.Imm(asm.R1, int32(bpfReadKernelSlot)),
			asm.Mov.Imm(asm.R2, int32(inst.OpCode.Size().Sizeof())),
			asm.Mov.Reg(asm.R3, inst.Src),
			asm.Add.Imm(asm.R3, int32(inst.Offset)),
			asm.FnProbeReadKernel.Call(),
			asm.LoadMem(inst.Dst, asm.RFP, bpfReadKernelSlot, inst.OpCode.Size()),
			// bpf_probe_read_kernel clobbers R4/R5; restore from stack.
			asm.LoadMem(asm.R4, asm.RFP, saveR4Slot, asm.DWord),
			asm.LoadMem(asm.R5, asm.RFP, saveR5Slot, asm.DWord),
		}
		restore := asm.Instructions{
			asm.LoadMem(asm.R1, asm.RFP, saveR1Slot, asm.DWord),
			asm.LoadMem(asm.R2, asm.RFP, saveR2Slot, asm.DWord),
			asm.LoadMem(asm.R3, asm.RFP, saveR3Slot, asm.DWord),
		}
		// Don't restore the destination register — it holds the loaded value.
		switch inst.Dst {
		case asm.R1:
			restore = restore[1:]
		case asm.R2:
			restore = asm.Instructions{restore[0], restore[2]}
		case asm.R3:
			restore = restore[:2]
		}
		repl = append(repl, restore...)
		repl[0].Metadata = inst.Metadata
		patches = append(patches, patch{idx: idx, repl: repl})
	}

	for i := len(patches) - 1; i >= 0; i-- {
		p := patches[i]
		insts = append(insts[:p.idx], append(p.repl, insts[p.idx+1:]...)...)
	}

	// Save R4/R5 at function entry before any bpf_probe_read_kernel clobbers them.
	insts = append(asm.Instructions{
		asm.StoreMem(asm.RFP, saveR4Slot, asm.R4, asm.DWord),
		asm.StoreMem(asm.RFP, saveR5Slot, asm.R5, asm.DWord),
	}, insts...)

	// Epilogue: cbpfc's final Ja.Label(resultLabel) lands here.
	// R0 holds the filter verdict (0=reject, nonzero=accept).
	// Set registers so the stub pass-through body evaluates to (verdict != 0):
	//   data(R4) != data_end(R5)  →  verdict != 0
	//   _ctx(R1)==__ctx(R2)==___ctx(R3)  →  0==0==0  →  true
	insts = append(
		insts,
		asm.Mov.Imm(asm.R1, 0).WithSymbol(resultLabel),
		asm.Mov.Imm(asm.R2, 0),
		asm.Mov.Imm(asm.R3, 0),
		asm.Mov.Reg(asm.R4, asm.R0),
		asm.Mov.Imm(asm.R5, 0),
		// No explicit Return: the stub pass-through body provides the return;
		// cbpfc's jump to resultLabel lands on the epilogue above.
	)

	return insts, nil
}

// compileCBPF compiles expr to cBPF via go-pcap (pure Go, no libpcap dependency).
// When l3 is true, packet offsets are retargeted from Ethernet-relative to raw-IP.
func compileCBPF(expr string, l3 bool) ([]cbpf.Instruction, error) {
	e := filter.NewExpression(expr)
	if e == nil {
		return nil, ErrEmptyFilter
	}
	f := e.Compile()
	if f == nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidFilter, expr)
	}
	insns, err := f.Compile()
	if err != nil {
		return nil, fmt.Errorf("go-pcap compile: %w", err)
	}
	if l3 {
		var l2Only bool
		insns, l2Only, err = retargetToL3(insns)
		if err != nil {
			return nil, err
		}
		if l2Only {
			return nil, fmt.Errorf("filter %q: %w", expr, ErrL2OnlyFilter)
		}
	}
	return boundJumps(insns), nil
}

// retargetToL3 retargets an Ethernet-relative cBPF program (as emitted by
// go-pcap) at raw-IP (DLT_RAW) packets by subtracting the Ethernet header
// length from every packet offset and rewriting the ethertype probe.
//
// The rewrite preserves instruction count, so all relative jump offsets in
// JumpIf/Jump remain valid. The second return value is true when the filter
// only matches L2 ethertypes (ARP/RARP/STP/…) that cannot appear on a raw
// L3 link, signaling the caller to substitute reject-all.
func retargetToL3(insns []cbpf.Instruction) ([]cbpf.Instruction, bool, error) {
	out := make([]cbpf.Instruction, len(insns))
	var sawProbe, anyPass bool
	for i, ins := range insns {
		switch v := ins.(type) {
		case cbpf.LoadAbsolute:
			// Offset 12 is the Ethernet ethertype field — no equivalent on DLT_RAW.
			// Replace with a constant that makes the following JumpIf behave correctly.
			if v.Off == 12 && v.Size == 2 {
				rep, pass, err := fakeEthertypeLoad(insns, i)
				if err != nil {
					return nil, false, err
				}
				sawProbe = true
				if pass {
					anyPass = true
				}
				out[i] = rep
				continue
			}
			v.Off = stripEtherOffset(v.Off)
			out[i] = v
		case cbpf.LoadIndirect:
			v.Off = stripEtherOffset(v.Off)
			out[i] = v
		case cbpf.LoadMemShift:
			v.Off = stripEtherOffset(v.Off)
			out[i] = v
		default:
			out[i] = ins
		}
	}
	return out, sawProbe && !anyPass, nil
}

// fakeEthertypeLoad returns a LoadConstant that stands in for the Ethernet
// ethertype load (LoadAbsolute{Off:12}) on a DLT_RAW link. It inspects the
// immediately following JumpIf and returns a constant that makes the jump
// behave correctly: true (pass) for IPv4/IPv6, false for everything else.
func fakeEthertypeLoad(insns []cbpf.Instruction, idx int) (cbpf.Instruction, bool, error) {
	if idx+1 >= len(insns) {
		return nil, false, fmt.Errorf("ethertype load at [%d]: no successor instruction", idx)
	}
	j, ok := insns[idx+1].(cbpf.JumpIf)
	if !ok {
		return nil, false, fmt.Errorf("ethertype load at [%d]: expected JumpIf, got %T", idx, insns[idx+1])
	}
	if j.Cond != cbpf.JumpEqual && j.Cond != cbpf.JumpNotEqual {
		return nil, false, fmt.Errorf("ethertype load at [%d]: unexpected JumpIf cond %v", idx, j.Cond)
	}
	switch j.Val {
	case etherTypeIPv4, etherTypeIPv6:
		return cbpf.LoadConstant{Dst: cbpf.RegA, Val: j.Val}, true, nil
	default:
		return cbpf.LoadConstant{Dst: cbpf.RegA, Val: 0}, false, nil
	}
}

// boundJumps rewrites out-of-bounds jump offsets to land on the last instruction.
// go-pcap follows libpcap convention where jumps past the end mean implicit drop;
// cbpfc requires all targets to be in-bounds. The last instruction is guaranteed
// to be RetConstant{Val:0}, so clamped jumps preserve the drop semantics.
func boundJumps(insns []cbpf.Instruction) []cbpf.Instruction {
	if len(insns) == 0 {
		return insns
	}
	// Defensive: ensure the trailing instruction is a drop, so clamped
	// jumps land on a drop (libpcap-style fall-through semantics).
	if r, ok := insns[len(insns)-1].(cbpf.RetConstant); !ok || r.Val != 0 {
		insns = append(insns, cbpf.RetConstant{Val: 0})
	}
	lastIdx := len(insns) - 1
	out := make([]cbpf.Instruction, len(insns))
	copy(out, insns)
	for i := range out {
		// valid target: i + 1 + skip <= lastIdx → skip <= lastIdx - i - 1
		maxSkip := lastIdx - i - 1
		if maxSkip < 0 {
			maxSkip = 0
		}
		switch v := out[i].(type) {
		case cbpf.JumpIf:
			v.SkipTrue, v.SkipFalse = clampJumpSkips(v.SkipTrue, v.SkipFalse, maxSkip)
			out[i] = v
		case cbpf.JumpIfX:
			v.SkipTrue, v.SkipFalse = clampJumpSkips(v.SkipTrue, v.SkipFalse, maxSkip)
			out[i] = v
		case cbpf.Jump:
			if v.Skip > uint32(maxSkip) {
				v.Skip = uint32(maxSkip)
			}
			out[i] = v
		}
	}
	return out
}

func stripEtherOffset(off uint32) uint32 {
	if off >= ethernetHeaderLen {
		return off - ethernetHeaderLen
	}
	return off
}

func clampJumpSkips(skipTrue, skipFalse uint8, maxSkip int) (uint8, uint8) {
	return uint8(min(int(skipTrue), maxSkip)), uint8(min(int(skipFalse), maxSkip))
}
