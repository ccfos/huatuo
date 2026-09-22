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

package memsnapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestProcessInstance(t *testing.T) {
	procRoot := identityProcRootForTest(t)
	pidDir := filepath.Join(procRoot, "42")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields("S " + strings.Repeat("0 ", 49))
	fields[19] = "999"
	stat := []byte("42 (worker) " + strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), stat, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := ProcessInstance{TGID: 42, StartTimeTicks: 999}
	if err := ValidateProcessInstance(identity); err != nil {
		t.Fatal(err)
	}
	identity.StartTimeTicks++
	if err := ValidateProcessInstance(identity); err == nil {
		t.Fatal("changed process identity was accepted")
	}
}

func TestReadProcMapsPreservesPath(t *testing.T) {
	for _, path := range []string{
		"", "/opt/runtime/libjvm.so", "/opt/runtime with spaces/libjvm.so",
		"/opt/runtime  with  spaces/libjvm.so", "/opt/runtime\twith\ttabs/libjvm.so",
		"/opt/runtime\u00a0name/libjvm.so", "/opt/library.so ",
		"/opt/runtime  name/libjvm.so (deleted)", "[heap]",
	} {
		t.Run(path, func(t *testing.T) {
			mapsPath := filepath.Join(t.TempDir(), "maps")
			line := "1000-2000 r-xp 00001000 08:01 42    " + path + "\n"
			if err := os.WriteFile(mapsPath, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			mappings, err := ReadProcMaps(mapsPath)
			if err != nil {
				t.Fatal(err)
			}
			want := ProcMap{
				Start: 0x1000, End: 0x2000, Offset: 0x1000,
				DevMajor: 8, DevMinor: 1, Inode: 42, Perms: "r-xp", Path: path,
			}
			if len(mappings) != 1 || mappings[0] != want {
				t.Fatalf("mappings = %#v, want %#v", mappings, want)
			}
		})
	}
}

func TestReadProcMapsSkipsMalformedLines(t *testing.T) {
	mapsPath := filepath.Join(t.TempDir(), "maps")
	data := "1000-2000 r-xp 00000000 08:01\n" +
		"2000-1000 r-xp 00000000 08:01 42 /invalid\n" +
		"1000-2000 r-xp 00000000 08:01 invalid /invalid\n" +
		"1000-2000 r-xp 00000000 08:01 42"
	if err := os.WriteFile(mapsPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	mappings, err := ReadProcMaps(mapsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].Inode != 42 || mappings[0].Path != "" {
		t.Fatalf("mappings = %#v, want one anonymous mapping", mappings)
	}
}

func TestFindLoadBiasMappingIdentity(t *testing.T) {
	target := ProcMap{Inode: 42, DevMajor: 8, DevMinor: 3}
	maps := []ProcMap{
		{Inode: 42, DevMajor: 9, DevMinor: 3, Start: 0x100000},
		{Inode: 42, DevMajor: 8, DevMinor: 2, Start: 0x200000},
		{Inode: 41, DevMajor: 8, DevMinor: 3, Start: 0x300000},
		{Inode: 42, DevMajor: 8, DevMinor: 3, Start: 0x80000000},
	}
	bias, err := FindLoadBias(maps, &target, 0, 0x1000)
	if err != nil || bias != 0x7ffff000 {
		t.Fatalf("bias = %#x, %v; want %#x", bias, err, uint64(0x7ffff000))
	}
	if _, err := FindLoadBias(maps[:3], &target, 0, 0); err == nil {
		t.Fatal("accepted an unrelated mapping with a colliding inode")
	}
	if _, err := FindLoadBias(maps, &target, 0x1000, 0); err == nil {
		t.Fatal("accepted a mapping with the wrong offset")
	}
	if _, err := FindLoadBias(maps, &target, 0, 0x90000000); err == nil {
		t.Fatal("accepted an underflowing relocation")
	}
}

func TestReadProcessInstanceStat(t *testing.T) {
	// Include the complete stat record because procfs parses more than starttime.
	fields := strings.Fields("S " + strings.Repeat("0 ", 49))
	fields[19] = "987654321"
	valid := "123 (worker (heap)) " + strings.Join(fields, " ")
	fields[19] = "0"
	zero := "123 (worker (heap)) " + strings.Join(fields, " ")

	for _, test := range []struct {
		name      string
		stat      string
		want      uint64
		wantError bool
	}{
		{name: "raw ticks and parentheses in comm", stat: valid, want: 987654321},
		{name: "zero starttime", stat: zero},
		{name: "truncated stat", stat: "123 (worker) S 0", wantError: true},
		{name: "missing stat", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			procRoot := identityProcRootForTest(t)
			// A real PID ensures the fixture detects accidental use of /proc.
			pid := os.Getpid()
			directory := filepath.Join(procRoot, strconv.Itoa(pid))
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.stat != "" {
				if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(test.stat), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := ReadProcessInstance(pid)
			got := identity.StartTimeTicks
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("starttime = %d, error = %v; want %d, error = %t", got, err, test.want, test.wantError)
			}
			if err == nil {
				if err := ValidateProcessInstance(identity); err != nil {
					t.Fatalf("validate read identity: %v", err)
				}
			}
			if test.stat == "" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing stat error = %v, want os.ErrNotExist", err)
			}
		})
	}
}

func TestReadProcessInstanceCurrentProcess(t *testing.T) {
	identity, err := ReadProcessInstance(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if identity.TGID != os.Getpid() {
		t.Fatalf("identity=%+v", identity)
	}
	if err := ValidateProcessInstance(identity); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{0, -1} {
		if _, err := ReadProcessInstance(pid); err == nil {
			t.Fatalf("accepted pid %d", pid)
		}
	}
}

// These tests are serial because RootPrefix changes the process-wide mount paths.
func identityProcRootForTest(t *testing.T) string {
	t.Helper()
	previous := filepath.Dir(procfs.DefaultPath())
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	if err := os.Mkdir(procRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(previous) })
	return procRoot
}
