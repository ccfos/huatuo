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

package pcapfilter

import (
	"fmt"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

// L2StubSymbol and L3StubSymbol are the __noinline BPF function names that mark
// the injection points. The C program must declare both pcap_stub_l2 and pcap_stub_l3.
const (
	L2StubSymbol = "pcap_stub_l2"
	L3StubSymbol = "pcap_stub_l3"
)

// Apply compiles filterExpr for both L2 (DLT_EN10MB) and L3 (DLT_RAW), then splices
// the resulting eBPF programs into their matching stub functions in spec.
// Must be called before ebpf.NewCollection.
//
// If filterExpr is empty, spec is left unchanged (stub pass-through = accept all).
func Apply(spec *ebpf.CollectionSpec, filterExpr string) error {
	if filterExpr == "" {
		return nil
	}

	l2Insts, l3Insts, err := buildL2L3FilterInsts(filterExpr)
	if err != nil {
		return err
	}

	if err := patchStub(spec, L2StubSymbol, l2Insts); err != nil {
		return err
	}
	return patchStub(spec, L3StubSymbol, l3Insts)
}

func patchStub(spec *ebpf.CollectionSpec, stubSymbol string, filterInsts asm.Instructions) error {
	for _, prog := range spec.Programs {
		for i, inst := range prog.Instructions {
			if inst.Symbol() != stubSymbol {
				continue
			}
			filterInsts[0] = filterInsts[0].WithMetadata(inst.Metadata)
			prog.Instructions[i] = inst.WithMetadata(asm.Metadata{})
			result := make(asm.Instructions, 0, len(prog.Instructions)+len(filterInsts))
			result = append(result, prog.Instructions[:i]...)
			result = append(result, filterInsts...)
			result = append(result, prog.Instructions[i:]...)
			prog.Instructions = result
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrStubNotFound, stubSymbol)
}
