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
// Filters are compiled natively for Ethernet and raw-IP packets. Tracing
// cannot provide the original skb length for nonlinear packets, so expressions
// requiring packet-length instructions (including byte-offset access) are rejected.
// Synthetic retransmit packets additionally lack Ethernet address fields.
package pcapfilter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cilium/ebpf/asm"
	"github.com/cloudflare/cbpfc"
	"github.com/huatuo-ai/go-pcap/filter"
	cbpf "golang.org/x/net/bpf"
)

var (
	// ErrEmptyFilter is returned when an empty string is passed as the filter expression.
	ErrEmptyFilter = errors.New("empty filter expression")
	// ErrInvalidFilter is returned when an expression cannot be compiled or
	// requires packet-length instructions unavailable to tracing.
	ErrInvalidFilter = errors.New("invalid filter expression")
	// ErrL3IncompatibleFilter is returned when a filter reads fields that are
	// unavailable in the synthetic raw-IP packet used by retransmission hooks.
	ErrL3IncompatibleFilter = errors.New(
		"filter requires packet fields unavailable to synthetic raw-IP packets",
	)
	// ErrStubNotFound is returned when the expected stub symbol is absent from the BPF object.
	ErrStubNotFound = errors.New("stub symbol not found")
)

// Ethernet address fields precede the EtherType at bytes 12..13.
const ethernetHeaderLen = 14

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
	l2Insts, err := buildFilterInsts(filterExpr, "_l2", filter.LinkTypeEthernet)
	if err != nil {
		return nil, nil, err
	}

	l3Insts, err := buildFilterInsts(filterExpr, "_l3", filter.LinkTypeRaw)
	if err != nil {
		return nil, nil, err
	}

	return l2Insts, l3Insts, nil
}

// ValidateL3Compatible rejects reads of fields that synthetic retransmit
// packets do not represent. EtherType predicates use the compiler's raw-IP layout.
func ValidateL3Compatible(filterExpr string) error {
	if strings.ContainsAny(filterExpr, "[]") {
		return fmt.Errorf("%w: byte-offset access is unavailable", ErrL3IncompatibleFilter)
	}

	insns, err := compileCBPF(filterExpr, filter.LinkTypeEthernet)
	if err != nil {
		return fmt.Errorf("validate L3 filter: %w", err)
	}

	for i, ins := range insns {
		var offset uint32
		switch load := ins.(type) {
		case cbpf.LoadExtension:
			if load.Num == cbpf.ExtLen {
				return fmt.Errorf("%w: original packet length is unavailable", ErrL3IncompatibleFilter)
			}
			continue
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

	_, err = compileCBPF(filterExpr, filter.LinkTypeRaw)
	return err
}

func buildFilterInsts(expr, suffix string, linkType filter.LinkType) (asm.Instructions, error) {
	cbpfInsns, err := compileCBPF(expr, linkType)
	if err != nil {
		return nil, fmt.Errorf("compile cBPF: %w", err)
	}

	// The stub's end-start is only the linear skb region, not the wire length.
	// Byte-access bounds checks also use ExtLen, so reject them consistently.
	for _, instruction := range cbpfInsns {
		if load, ok := instruction.(cbpf.LoadExtension); ok && load.Num == cbpf.ExtLen {
			return nil, fmt.Errorf("%w: packet-length instructions are unsupported by tracing", ErrInvalidFilter)
		}
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

// compileCBPF keeps the package's error identities while using native packet
// layouts. L2-only predicates remain reject-all on raw IP; negations and mixed
// expressions retain the compiler's boolean semantics.
func compileCBPF(expr string, linkType filter.LinkType) ([]cbpf.Instruction, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, ErrEmptyFilter
	}

	insns, err := filter.Compile(expr, linkType)
	if linkType == filter.LinkTypeRaw && errors.Is(err, filter.ErrL2OnlyLinkType) {
		return []cbpf.Instruction{cbpf.RetConstant{Val: 0}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFilter, err)
	}

	return insns, nil
}
