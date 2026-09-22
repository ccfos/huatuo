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
	"os"
	"path/filepath"
	"testing"
)

func TestProcessIdentity(t *testing.T) {
	procRoot := t.TempDir()
	pidDir := filepath.Join(procRoot, "42")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := []byte("42 (worker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 999")
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), stat, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := ProcessIdentity{TGID: 42, StartTimeTicks: 999}
	if err := ValidateIdentity(procRoot, identity); err != nil {
		t.Fatal(err)
	}
	identity.StartTimeTicks++
	if err := ValidateIdentity(procRoot, identity); err == nil {
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
