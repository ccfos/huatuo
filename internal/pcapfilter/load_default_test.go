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
	"flag"
	"os"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"golang.org/x/sys/unix"
)

var debug = flag.Bool("debug", false, "dump pcapfilter test program details")

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

	if _, err := bpf.LoadBpfFromCollectionSpec("dropwatch-spec.o", specs, nil); err != nil {
		t.Fatalf("load bpf: %v", err)
	}
}

func TestCompileL2L3L2OnlyFallback(t *testing.T) {
	l2Insts, l3Insts, err := buildL2L3FilterInsts("arp")
	if err != nil {
		t.Fatalf("compile L2/L3 filters: %v", err)
	}

	if len(l2Insts) == 0 {
		t.Fatalf("expected L2 instructions for arp filter")
	}

	want := asm.Instructions{asm.Mov.Reg(asm.R4, asm.R5)}
	if len(l3Insts) != len(want) {
		t.Fatalf("unexpected L3 fallback length: got %d want %d", len(l3Insts), len(want))
	}

	for i := range want {
		if l3Insts[i].OpCode != want[i].OpCode || l3Insts[i].Dst != want[i].Dst || l3Insts[i].Src != want[i].Src {
			t.Fatalf("unexpected L3 fallback instruction at %d: got %+v want %+v", i, l3Insts[i], want[i])
		}
	}
}
