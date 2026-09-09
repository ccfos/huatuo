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

package capacity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t testing.TB, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, root, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}

func fixture(t testing.TB, unified bool) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".", "parent", "parent/leaf"} {
		if unified {
			if dir != "." {
				writeFile(t, root, filepath.Join(dir, "cpu.max"), "max 100000")
			}
			writeFile(t, root, filepath.Join(dir, "cpuset.cpus.effective"), "0-7")
		} else {
			writeFile(t, root, filepath.Join("cpu", dir, "cpu.cfs_quota_us"), "-1")
			writeFile(t, root, filepath.Join("cpu", dir, "cpu.cfs_period_us"), "100000")
			writeFile(t, root, filepath.Join("cpuacct", dir, "cpuacct.usage"), "1000000000")
			writeFile(t, root, filepath.Join("cpuset", dir, "cpuset.cpus"), "0-7")
		}
	}
	return root
}

func TestCapacityHierarchy(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(fmt.Sprintf("v2=%v", unified), func(t *testing.T) {
			root := fixture(t, unified)
			if unified {
				writeFile(t, root, "parent/cpu.max", "150000 100000")
				writeFile(t, root, "parent/leaf/cpu.max", "400000 100000")
			} else {
				writeFile(t, root, "cpu/parent/cpu.cfs_quota_us", "150000")
				writeFile(t, root, "cpu/parent/leaf/cpu.cfs_quota_us", "400000")
			}
			got, err := read(root, "/parent/leaf", "0-7\n", unified)
			if err != nil || got.Cores != 1.5 {
				t.Fatalf("read() = %v, %v, want 1.5 cores", got, err)
			}
			again, err := read(root, "/parent/leaf", "0-7\n", unified)
			if err != nil || *again != *got {
				t.Fatalf("unchanged configuration = %v, %v, want %v", again, err, got)
			}
		})
	}
}

func TestCapacityCpuset(t *testing.T) {
	tests := []struct {
		name    string
		unified bool
		files   map[string]string
		remove  []string
		online  string
		want    float64
		wantErr bool
	}{
		{name: "v1 online intersection", files: map[string]string{"cpuset/parent/leaf/cpuset.cpus": "2-5"}, online: "0-3", want: 2},
		{name: "v1 disjoint", files: map[string]string{"cpuset/parent/leaf/cpuset.cpus": "4-7"}, online: "0-3", wantErr: true},
		{name: "v1 effective preferred", files: map[string]string{"cpuset/parent/leaf/cpuset.effective_cpus": "1-2"}, want: 2},
		{name: "v1 empty requested inherits", files: map[string]string{"cpuset/parent/leaf/cpuset.cpus": "\n", "cpuset/parent/cpuset.cpus": "0-2"}, want: 3},
		{name: "v1 empty effective does not inherit", files: map[string]string{"cpuset/parent/leaf/cpuset.effective_cpus": ""}, wantErr: true},
		{name: "v1 empty root", files: map[string]string{"cpuset/cpuset.cpus": ""}, wantErr: true},
		{name: "v1 missing configured file", remove: []string{"cpuset/parent/leaf/cpuset.cpus"}, wantErr: true},
		{name: "v2 effective intersection", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": "2-5"}, online: "0-3", want: 2},
		{name: "v2 disjoint partitions", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": "2-3", "parent/cpuset.cpus.effective": "0-1"}, want: 2},
		{name: "v2 empty parent partition", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": "2-3", "parent/cpuset.cpus.effective": ""}, want: 2},
		{name: "v2 empty root with child CPUs", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": "2-3", "cpuset.cpus.effective": ""}, want: 2},
		{name: "v2 nearest available ancestor", unified: true, files: map[string]string{"parent/cpuset.cpus.effective": "0-2"}, remove: []string{"parent/leaf/cpuset.cpus.effective"}, want: 3},
		{name: "v2 inherits partition CPUs", unified: true, files: map[string]string{"parent/cpuset.cpus.effective": "2-3", "cpuset.cpus.effective": "0-1"}, remove: []string{"parent/leaf/cpuset.cpus.effective"}, want: 2},
		{name: "v2 empty nearest available ancestor", unified: true, files: map[string]string{"parent/cpuset.cpus.effective": ""}, remove: []string{"parent/leaf/cpuset.cpus.effective"}, wantErr: true},
		{name: "v2 root only", unified: true, files: map[string]string{"cpuset.cpus.effective": "0-1"}, remove: []string{"parent/leaf/cpuset.cpus.effective", "parent/cpuset.cpus.effective"}, want: 2},
		{name: "v2 unavailable controller", unified: true, remove: []string{"parent/leaf/cpuset.cpus.effective", "parent/cpuset.cpus.effective", "cpuset.cpus.effective"}, want: 8},
		{name: "v2 empty effective", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": ""}, wantErr: true},
		{name: "invalid cpuset", unified: true, files: map[string]string{"parent/leaf/cpuset.cpus.effective": "3-1"}, wantErr: true},
		{name: "invalid online", unified: true, online: "bad", wantErr: true},
		{name: "empty online", unified: true, online: "\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := fixture(t, tt.unified)
			for name, value := range tt.files {
				writeFile(t, root, name, value)
			}
			for _, name := range tt.remove {
				removeFile(t, root, name)
			}
			online := tt.online
			if online == "" {
				online = "0-7"
			}
			got, err := read(root, "parent/leaf", online, tt.unified)
			if (err != nil) != tt.wantErr {
				t.Fatalf("read() = %v, %v, want error %v", got, err, tt.wantErr)
			}
			if err == nil && got.Cores != tt.want {
				t.Fatalf("read() cores = %v, want %v", got.Cores, tt.want)
			}
		})
	}
}

func TestCapacityInvalidQuota(t *testing.T) {
	tests := []struct {
		name    string
		unified bool
		file    string
		value   string
		remove  bool
	}{
		{name: "v2 missing leaf", unified: true, file: "parent/leaf/cpu.max", remove: true},
		{name: "v2 missing ancestor", unified: true, file: "parent/cpu.max", remove: true},
		{name: "v2 incomplete", unified: true, file: "parent/cpu.max", value: "max"},
		{name: "v2 extra field", unified: true, file: "parent/cpu.max", value: "max 100000 extra"},
		{name: "v2 zero quota", unified: true, file: "parent/cpu.max", value: "0 100000"},
		{name: "v2 zero period", unified: true, file: "parent/cpu.max", value: "max 0"},
		{name: "v2 invalid period", unified: true, file: "parent/cpu.max", value: "max bad"},
		{name: "v2 negative", unified: true, file: "parent/cpu.max", value: "-1 100000"},
		{name: "v2 overflow", unified: true, file: "parent/cpu.max", value: "18446744073709551616 100000"},
		{name: "v1 missing quota", file: "cpu/parent/cpu.cfs_quota_us", remove: true},
		{name: "v1 missing period", file: "cpu/parent/cpu.cfs_period_us", remove: true},
		{name: "v1 zero quota", file: "cpu/parent/cpu.cfs_quota_us", value: "0"},
		{name: "v1 invalid unlimited", file: "cpu/parent/cpu.cfs_quota_us", value: "-2"},
		{name: "v1 signed overflow", file: "cpu/parent/cpu.cfs_quota_us", value: "9223372036854775808"},
		{name: "v1 invalid period", file: "cpu/parent/cpu.cfs_period_us", value: "bad"},
		{name: "v1 zero period", file: "cpu/parent/cpu.cfs_period_us", value: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := fixture(t, tt.unified)
			if tt.remove {
				removeFile(t, root, tt.file)
			} else {
				writeFile(t, root, tt.file, tt.value)
			}
			if got, err := read(root, "parent/leaf", "0-7", tt.unified); err == nil {
				t.Fatalf("read() = %v, want error for %s", got, tt.file)
			}
		})
	}
}

func TestCapacityIdentityChanges(t *testing.T) {
	changes := map[string]func(*testing.T, string){
		"disjoint ancestor cpuset":      func(t *testing.T, root string) { writeFile(t, root, "parent/cpuset.cpus.effective", "4-7") },
		"ancestor cpuset becomes empty": func(t *testing.T, root string) { writeFile(t, root, "parent/cpuset.cpus.effective", "") },
		"equal ratio":                   func(t *testing.T, root string) { writeFile(t, root, "parent/cpu.max", "400000 200000") },
		"nonbinding quota":              func(t *testing.T, root string) { writeFile(t, root, "parent/leaf/cpu.max", "600000 100000") },
		"equal count cpuset swap":       func(t *testing.T, root string) { writeFile(t, root, "parent/leaf/cpuset.cpus.effective", "2-5") },
		"root max appears":              func(t *testing.T, root string) { writeFile(t, root, "cpu.max", "max 100000") },
		"cpuset becomes inherited":      func(t *testing.T, root string) { removeFile(t, root, "parent/leaf/cpuset.cpus.effective") },
		"directory recreated": func(t *testing.T, root string) {
			if err := os.Rename(filepath.Join(root, "parent/leaf"), filepath.Join(root, "parent/previous")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, root, "parent/leaf/cpu.max", "max 100000")
			writeFile(t, root, "parent/leaf/cpuset.cpus.effective", "0-3")
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			root := fixture(t, true)
			writeFile(t, root, "parent/cpu.max", "200000 100000")
			writeFile(t, root, "parent/leaf/cpuset.cpus.effective", "0-3")
			before, err := ReadV2(root, "parent/leaf", "0-7")
			if err != nil {
				t.Fatal(err)
			}
			change(t, root)
			after, err := ReadV2(root, "parent/leaf", "0-7")
			if err != nil || after.Cores != before.Cores || after.ConfigID == before.ConfigID {
				t.Fatalf("before = %v, after = %v, error = %v; want equal cores, different identity", before, after, err)
			}
		})
	}
	t.Run("equal count online swap", func(t *testing.T) {
		root := fixture(t, true)
		before, err := ReadV2(root, "parent/leaf", "0-3")
		if err != nil {
			t.Fatal(err)
		}
		after, err := ReadV2(root, "parent/leaf", "4-7")
		if err != nil || after.Cores != before.Cores || after.ConfigID == before.ConfigID {
			t.Fatalf("before = %v, after = %v, error = %v", before, after, err)
		}
	})
}

func TestCapacityRootBoundary(t *testing.T) {
	t.Run("visible root does not include parent limits", func(t *testing.T) {
		root := fixture(t, true)
		writeFile(t, root, "parent/cpu.max", "100000 100000")
		got, err := ReadV2(filepath.Join(root, "parent/leaf"), "", "0-7")
		if err != nil || got.Cores != 8 {
			t.Fatalf("ReadV2() = %v, %v, want 8 cores", got, err)
		}
	})
	t.Run("v1 controller alias", func(t *testing.T) {
		root := fixture(t, false)
		if err := os.Rename(filepath.Join(root, "cpu"), filepath.Join(root, "cpu,cpuacct")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("cpu,cpuacct", filepath.Join(root, "cpu")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "cpuacct"), filepath.Join(root, "cpuacct-saved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("cpu,cpuacct", filepath.Join(root, "cpuacct")); err != nil {
			t.Fatal(err)
		}
		got, err := ReadV1(root, "parent/leaf", "0-7")
		if err != nil || got.Cores != 8 {
			t.Fatalf("ReadV1() = %v, %v, want 8 cores", got, err)
		}
	})
	for _, state := range []string{"absent", "missing leaf", "broken alias"} {
		t.Run("v1 cpuset "+state, func(t *testing.T) {
			root := fixture(t, false)
			old := filepath.Join(root, "cpuset")
			if state == "missing leaf" {
				old = filepath.Join(old, "parent/leaf")
			}
			if err := os.Rename(old, old+"-saved"); err != nil {
				t.Fatal(err)
			}
			if state == "broken alias" {
				if err := os.Symlink("missing", old); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadV1(root, "parent/leaf", "0-7")
			if state == "absent" {
				if err != nil || got.Cores != 8 {
					t.Fatalf("ReadV1() = %v, %v, want 8 cores", got, err)
				}
			} else if err == nil {
				t.Fatalf("ReadV1() = %v, want error", got)
			}
		})
	}
	for _, path := range []string{"../parent/leaf", "parent/../parent/leaf", "missing", "parent/cpu.max"} {
		t.Run(path, func(t *testing.T) {
			if got, err := ReadV2(fixture(t, true), path, "0-7"); err == nil {
				t.Fatalf("ReadV2() = %v, want invalid path error", got)
			}
		})
	}
	for _, target := range []string{"directory", "control file"} {
		t.Run("escaping "+target, func(t *testing.T) {
			root := fixture(t, true)
			outside := fixture(t, true)
			link := filepath.Join(root, "parent/leaf")
			destination := filepath.Join(outside, "parent/leaf")
			if target == "control file" {
				link = filepath.Join(link, "cpu.max")
				destination = filepath.Join(destination, "cpu.max")
			}
			if err := os.Rename(link, link+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadV2(root, "parent/leaf", "0-7"); err == nil {
				t.Fatalf("ReadV2() = %v, want symlink escape error", got)
			}
		})
	}
}

func TestCapacityCPUAcctGeneration(t *testing.T) {
	root := fixture(t, false)
	before, err := ReadV1(root, "parent/leaf", "0-7")
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "cpuacct/parent/leaf")
	if err := os.Rename(leaf, leaf+"-saved"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadV1(root, "parent/leaf", "0-7"); err == nil {
		t.Fatalf("ReadV1() = %v, want missing cpuacct error", got)
	}
	writeFile(t, root, "cpuacct/parent/leaf/cpuacct.usage", "2000000000")
	after, err := ReadV1(root, "parent/leaf", "0-7")
	if err != nil || after.Cores != before.Cores || after.ConfigID == before.ConfigID {
		t.Fatalf("before = %v, after = %v, error = %v; want new cpuacct generation", before, after, err)
	}
	writeFile(t, root, "cpuacct/parent/leaf/cpuacct.usage", "3000000000")
	again, err := ReadV1(root, "parent/leaf", "0-7")
	if err != nil || *again != *after {
		t.Fatalf("usage-only change = %v, %v; want unchanged identity %v", again, err, after)
	}
}

func TestCapacityReadErrors(t *testing.T) {
	for _, name := range []string{"parent/cpu.max", "parent/cpuset.cpus.effective"} {
		t.Run(name, func(t *testing.T) {
			root := fixture(t, true)
			removeFile(t, root, name)
			if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadV2(root, "parent/leaf", "0-7"); err == nil {
				t.Fatalf("ReadV2() = %v, want read error", got)
			}
		})
	}
	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read mode-000 files")
		}
		root := fixture(t, true)
		if err := os.Chmod(filepath.Join(root, "parent/cpuset.cpus.effective"), 0); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadV2(root, "parent/leaf", "0-7"); err == nil {
			t.Fatalf("ReadV2() = %v, want permission error", got)
		}
	})
}

func BenchmarkCapacity(b *testing.B) {
	for _, unified := range []bool{false, true} {
		for _, count := range []int{100, 1000} {
			for _, depth := range []int{2, 5} {
				b.Run(fmt.Sprintf("v2=%v/containers=%d/depth=%d", unified, count, depth), func(b *testing.B) {
					root := b.TempDir()
					paths := make([]string, count)
					for index := range paths {
						paths[index] = strings.Repeat("parent/", depth-1) + fmt.Sprintf("leaf%d", index)
						for dir := paths[index]; ; dir = filepath.Dir(dir) {
							if unified {
								if dir != "." {
									writeFile(b, root, filepath.Join(dir, "cpu.max"), "200000 100000")
								}
								writeFile(b, root, filepath.Join(dir, "cpuset.cpus.effective"), "0-7")
							} else {
								writeFile(b, root, filepath.Join("cpu", dir, "cpu.cfs_quota_us"), "200000")
								writeFile(b, root, filepath.Join("cpu", dir, "cpu.cfs_period_us"), "100000")
								writeFile(b, root, filepath.Join("cpuacct", dir, "cpuacct.usage"), "1000000000")
								writeFile(b, root, filepath.Join("cpuset", dir, "cpuset.cpus"), "0-7")
							}
							if dir == "." {
								break
							}
						}
					}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						for _, path := range paths {
							if _, err := read(root, path, "0-7", unified); err != nil {
								b.Fatal(err)
							}
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*count), "ns/container")
				})
			}
		}
	}
}
