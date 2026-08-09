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
	"errors"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

func TestPatchStubSplicesFilterBeforeStub(t *testing.T) {
	stub := asm.Mov.Imm(asm.R0, 1).WithSymbol(L2StubSymbol)
	program := &ebpf.ProgramSpec{Instructions: asm.Instructions{stub, asm.Return()}}
	spec := &ebpf.CollectionSpec{Programs: map[string]*ebpf.ProgramSpec{"test": program}}
	filter := asm.Instructions{asm.Mov.Imm(asm.R0, 0), asm.Return()}

	if err := patchStub(spec, L2StubSymbol, filter); err != nil {
		t.Fatalf("patchStub() error = %v", err)
	}
	if got, want := len(program.Instructions), 4; got != want {
		t.Fatalf("instruction count = %d, want %d", got, want)
	}
	if got := program.Instructions[2].Symbol(); got != L2StubSymbol {
		t.Errorf("stub symbol = %q, want %q", got, L2StubSymbol)
	}
}

func TestPatchStubReportsMissingSymbol(t *testing.T) {
	spec := &ebpf.CollectionSpec{Programs: map[string]*ebpf.ProgramSpec{"test": {Instructions: asm.Instructions{asm.Return()}}}}
	if err := patchStub(spec, L3StubSymbol, asm.Instructions{asm.Return()}); !errors.Is(err, ErrStubNotFound) {
		t.Fatalf("patchStub() error = %v, want %v", err, ErrStubNotFound)
	}
}
