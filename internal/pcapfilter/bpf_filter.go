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
	// ErrL3IncompatibleFilter is returned when a filter reads fields that are
	// unavailable in the synthetic raw-IP packet used by local correlation.
	ErrL3IncompatibleFilter = errors.New(
		"filter requires ethernet header fields unavailable to local correlation",
	)
	// ErrStubNotFound is returned when the expected stub symbol is absent from the BPF object.
	ErrStubNotFound = errors.New("stub symbol not found")
)

const (
	// ethernetHeaderLen is the L2 Ethernet header length assumed by go-pcap's
	// filter compiler. We subtract it from packet offsets to retarget filters
	// at L3 (DLT_RAW) packets.
	ethernetHeaderLen = 14

	etherTypeIPv4    = 0x0800
	etherTypeIPv6    = 0x86dd
	rawIPVersionMask = 0xf0
	rawIPv4Version   = 4 << 4
	rawIPv6Version   = 6 << 4
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
		return nil, nil, err
	}

	return l2Insts, l3Insts, nil
}

// ValidateL3Compatible compiles an expression and rejects semantic packet
// reads from the Ethernet address region. Ethertype reads remain valid because
// retargetToL3 rewrites them to raw-IP version checks.
func ValidateL3Compatible(filterExpr string) error {
	insns, err := compileCBPF(filterExpr, false)
	if err != nil {
		return fmt.Errorf("validate L3 filter: %w", err)
	}
	for i, ins := range insns {
		var offset uint32
		switch load := ins.(type) {
		case cbpf.LoadAbsolute:
			if load.Off == 12 && load.Size == 2 {
				continue
			}
			offset = load.Off
		case cbpf.LoadIndirect:
			offset = load.Off
		case cbpf.LoadMemShift:
			offset = load.Off
		default:
			continue
		}
		if offset < ethernetHeaderLen {
			return fmt.Errorf(
				"%w: instruction %d reads Ethernet byte %d",
				ErrL3IncompatibleFilter,
				i,
				offset,
			)
		}
	}
	return nil
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
		insns, err = retargetToL3(insns)
		if err != nil {
			return nil, err
		}
	}
	return boundJumps(insns), nil
}

// retargetToL3 retargets an Ethernet-relative cBPF program (as emitted by
// go-pcap) at raw-IP (DLT_RAW) packets by subtracting the Ethernet header
// length from every packet offset and rewriting the ethertype probe.
//
// IP ethertype probes become a version-nibble load and mask, so the rewrite
// remaps every relative jump across the inserted instruction. L2-only
// comparisons remain unchanged and therefore naturally reject raw IP packets;
// their negations naturally accept them.
func retargetToL3(insns []cbpf.Instruction) ([]cbpf.Instruction, error) {
	if len(insns) == 0 {
		return []cbpf.Instruction{}, nil
	}

	out := make([]cbpf.Instruction, 0, len(insns))
	newIndexes := make([]int, len(insns))
	ethertypeComparisons := findEthertypeComparisons(insns)
	for i, ins := range insns {
		newIndexes[i] = len(out)
		switch v := ins.(type) {
		case cbpf.LoadAbsolute:
			if v.Off == 12 && v.Size == 2 {
				if i+1 >= len(insns) {
					return nil, fmt.Errorf(
						"ethertype load at [%d]: no successor instruction",
						i,
					)
				}
				jump, ok := insns[i+1].(cbpf.JumpIf)
				if !ok {
					return nil, fmt.Errorf(
						"ethertype load at [%d]: expected JumpIf, got %T",
						i,
						insns[i+1],
					)
				}
				if jump.Cond != cbpf.JumpEqual && jump.Cond != cbpf.JumpNotEqual {
					return nil, fmt.Errorf(
						"ethertype load at [%d]: unexpected JumpIf cond %v",
						i,
						jump.Cond,
					)
				}
				out = append(
					out,
					cbpf.LoadAbsolute{Off: 0, Size: 1},
					cbpf.ALUOpConstant{Op: cbpf.ALUOpAnd, Val: rawIPVersionMask},
				)
				continue
			}
			v.Off = stripEtherOffset(v.Off)
			out = append(out, v)
		case cbpf.LoadIndirect:
			v.Off = stripEtherOffset(v.Off)
			out = append(out, v)
		case cbpf.LoadMemShift:
			v.Off = stripEtherOffset(v.Off)
			out = append(out, v)
		case cbpf.JumpIf:
			if ethertypeComparisons[i] {
				switch v.Val {
				case etherTypeIPv4:
					v.Val = rawIPv4Version
				case etherTypeIPv6:
					v.Val = rawIPv6Version
				}
			}
			out = append(out, v)
		default:
			out = append(out, ins)
		}
	}

	for oldIndex := range insns {
		newIndex := newIndexes[oldIndex]
		switch jump := out[newIndex].(type) {
		case cbpf.JumpIf:
			trueSkip, err := remapL3JumpSkip(
				oldIndex,
				uint32(jump.SkipTrue),
				len(insns),
				newIndexes,
			)
			if err != nil {
				return nil, err
			}
			falseSkip, err := remapL3JumpSkip(
				oldIndex,
				uint32(jump.SkipFalse),
				len(insns),
				newIndexes,
			)
			if err != nil {
				return nil, err
			}
			if trueSkip > 255 || falseSkip > 255 {
				return nil, fmt.Errorf(
					"retarget L3 jump at [%d]: expanded conditional skip exceeds 255",
					oldIndex,
				)
			}
			jump.SkipTrue = uint8(trueSkip)
			jump.SkipFalse = uint8(falseSkip)
			out[newIndex] = jump
		case cbpf.JumpIfX:
			trueSkip, err := remapL3JumpSkip(
				oldIndex,
				uint32(jump.SkipTrue),
				len(insns),
				newIndexes,
			)
			if err != nil {
				return nil, err
			}
			falseSkip, err := remapL3JumpSkip(
				oldIndex,
				uint32(jump.SkipFalse),
				len(insns),
				newIndexes,
			)
			if err != nil {
				return nil, err
			}
			if trueSkip > 255 || falseSkip > 255 {
				return nil, fmt.Errorf(
					"retarget L3 jump at [%d]: expanded conditional skip exceeds 255",
					oldIndex,
				)
			}
			jump.SkipTrue = uint8(trueSkip)
			jump.SkipFalse = uint8(falseSkip)
			out[newIndex] = jump
		case cbpf.Jump:
			skip, err := remapL3JumpSkip(
				oldIndex,
				jump.Skip,
				len(insns),
				newIndexes,
			)
			if err != nil {
				return nil, err
			}
			jump.Skip = skip
			out[newIndex] = jump
		}
	}
	return out, nil
}

// findEthertypeComparisons follows branches until register A is overwritten.
// go-pcap may reuse one ethertype load for non-adjacent IPv6 and IPv4 tests.
func findEthertypeComparisons(insns []cbpf.Instruction) []bool {
	comparisons := make([]bool, len(insns))
	visited := make([]bool, len(insns))
	var pending []int
	for i, ins := range insns {
		load, ok := ins.(cbpf.LoadAbsolute)
		if !ok || load.Off != 12 || load.Size != 2 {
			continue
		}
		pending = append(pending, i+1)
	}

	pushTarget := func(index int, skip uint32) {
		target := uint64(index) + 1 + uint64(skip)
		if target >= uint64(len(insns)) {
			target = uint64(len(insns) - 1)
		}
		pending = append(pending, int(target))
	}
	for len(pending) != 0 {
		last := len(pending) - 1
		index := pending[last]
		pending = pending[:last]
		if index < 0 || index >= len(insns) || visited[index] {
			continue
		}
		visited[index] = true

		switch ins := insns[index].(type) {
		case cbpf.JumpIf:
			comparisons[index] = true
			pushTarget(index, uint32(ins.SkipTrue))
			pushTarget(index, uint32(ins.SkipFalse))
		case cbpf.JumpIfX:
			pushTarget(index, uint32(ins.SkipTrue))
			pushTarget(index, uint32(ins.SkipFalse))
		case cbpf.Jump:
			pushTarget(index, ins.Skip)
		case cbpf.LoadConstant:
			if ins.Dst == cbpf.RegX {
				pending = append(pending, index+1)
			}
		case cbpf.LoadScratch:
			if ins.Dst == cbpf.RegX {
				pending = append(pending, index+1)
			}
		case cbpf.LoadMemShift, cbpf.StoreScratch, cbpf.TAX:
			pending = append(pending, index+1)
		}
	}
	return comparisons
}

func remapL3JumpSkip(
	oldIndex int,
	oldSkip uint32,
	oldLength int,
	newIndexes []int,
) (uint32, error) {
	target := oldLength - 1
	requestedTarget := uint64(oldIndex) + 1 + uint64(oldSkip)
	if requestedTarget < uint64(oldLength) {
		target = int(requestedTarget)
	}
	newSkip := newIndexes[target] - newIndexes[oldIndex] - 1
	if newSkip < 0 {
		return 0, fmt.Errorf(
			"retarget L3 jump at [%d]: target [%d] is not forward",
			oldIndex,
			target,
		)
	}
	return uint32(newSkip), nil
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
