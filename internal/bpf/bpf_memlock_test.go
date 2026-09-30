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

package bpf

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

func TestInitWithMemcgAndFiniteMemlock(t *testing.T) {
	requireBPFPermission(t)
	if os.Getenv("HUATUO_TEST_FINITE_MEMLOCK") != "1" {
		if os.Geteuid() != 0 {
			t.Skip("requires a capability-limited root subprocess")
		}
		setpriv, err := exec.LookPath("setpriv")
		if err != nil {
			t.Skip("setpriv is required to drop CAP_SYS_RESOURCE")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, setpriv, "--bounding-set=-sys_resource", os.Args[0], "-test.run=^TestInitWithMemcgAndFiniteMemlock$", "-test.v")
		cmd.Env = append(os.Environ(), "HUATUO_TEST_FINITE_MEMLOCK=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("finite-memlock subprocess: %v\n%s", err, out)
		}
		t.Logf("%s", out)
		return
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &original); err != nil {
		t.Fatal(err)
	}
	// This subprocess cannot raise its hard limit again. A map that still loads
	// with zero memlock proves that its kernel uses another accounting mechanism.
	finite := unix.Rlimit{Cur: 0, Max: min(original.Max, 65536)}
	if err := unix.Setrlimit(unix.RLIMIT_MEMLOCK, &finite); err != nil {
		t.Fatal(err)
	}
	probe, err := ebpf.NewMap(&ebpf.MapSpec{Type: ebpf.Array, KeySize: 4, ValueSize: 4, MaxEntries: 1})
	if errors.Is(err, unix.EPERM) {
		t.Skip("kernel still charges BPF maps to memlock")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Init(nil); err != nil {
		t.Fatalf("Init on a memcg-accounted kernel: %v", err)
	}
	var after unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &after); err != nil {
		t.Fatal(err)
	}
	if after != finite {
		t.Fatalf("memlock changed: before=%+v after=%+v", finite, after)
	}
	Shutdown()
}
