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

package symbol

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/elfmapping"
	"github.com/ccfos/huatuo/internal/procfs"
)

func requireExternalGoCompiler(t testing.TB) {
	t.Helper()
	out, err := exec.Command("go", "env", "CC").Output()
	if err != nil {
		t.Fatalf("determine external Go compiler: %v", err)
	}
	cc := strings.TrimSpace(string(out))
	if cc == "" {
		t.Fatal("go env CC returned an empty compiler command")
	}
	// Leave wrapper arguments and quoting to Go's CC parser.
	if !strings.ContainsAny(cc, " \t\r\n\"'\\") {
		if _, err := exec.LookPath(cc); err != nil {
			t.Skipf("external Go linking requires CC=%s: %v", cc, err)
		}
	}
}

func TestReadGoTable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Go fixture  with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "main.go")
	fixture := `package main
import ("fmt"; "os"; "reflect")
func main() {
 fmt.Printf("%x\n", reflect.ValueOf(main).Pointer())
 maps, err := os.ReadFile("/proc/self/maps")
 if err != nil { panic(err) }
 fmt.Print(string(maps))
}
`
	if err := os.WriteFile(source, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var builds []struct{ link, mode string }
	for _, link := range []string{"internal", "external"} {
		for _, mode := range []string{"exe", "pie", "stripped", "stripped-pie"} {
			builds = append(builds, struct{ link, mode string }{
				link: link,
				mode: mode,
			})
		}
	}
	for _, build := range builds {
		t.Run(build.link+"/"+build.mode, func(t *testing.T) {
			mode := build.mode
			if build.link == "external" {
				requireExternalGoCompiler(t)
			}
			path := filepath.Join(dir, build.link+"-"+mode)
			args := []string{"build", "-o", path}
			if mode == "pie" || mode == "stripped-pie" {
				args = append(args, "-buildmode=pie")
			}
			ldflags := "-linkmode=" + build.link
			if mode == "stripped" || mode == "stripped-pie" {
				ldflags += " -s -w"
			}
			args = append(args, "-ldflags="+ldflags, source)
			command := exec.Command("go", args...)
			cgo := "0"
			if build.link == "external" {
				cgo = "1"
			}
			command.Env = append(os.Environ(), "CGO_ENABLED="+cgo)
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("build: %v: %s", err, out)
			}
			file, err := elf.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			table, err := ReadGoTable(t.Context(), file)
			if err != nil {
				t.Fatal(err)
			}
			fn := table.LookupFunc("main.main")
			if fn == nil || table.PCToFunc(fn.Entry) != fn {
				t.Fatal("missing main.main at link-time PC")
			}
			// The running program supplies the ground truth, independent of
			// pclntab decoding. A self-consistent but shifted table must fail.
			out, err := exec.Command(path).Output()
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(out), "\n")
			pc, err := strconv.ParseUint(lines[0], 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			runtimePC := pc
			var mapped bool
			for _, line := range lines[1:] {
				fields := strings.Fields(line)
				if len(fields) < 6 || fields[2] != "00000000" {
					continue
				}
				pathname := line
				for _, field := range fields[:5] {
					pathname = strings.TrimPrefix(strings.TrimLeft(pathname, " \t"), field)
				}
				if strings.TrimLeft(pathname, " \t") != path {
					continue
				}
				start, _, _ := strings.Cut(fields[0], "-")
				address, err := strconv.ParseUint(start, 16, 64)
				if err != nil {
					t.Fatal(err)
				}
				for _, program := range file.Progs {
					if program.Type == elf.PT_LOAD && program.Off == 0 {
						pc -= address - program.Vaddr
						mapped = true
						break
					}
				}
				break
			}
			if !mapped || fn.Entry != pc || table.PCToFunc(pc) != fn {
				t.Fatalf("Go entry=%#x, runtime entry=%#x, mapping found=%t", fn.Entry, pc, mapped)
			}
			if file.Type == elf.ET_DYN {
				t.Run("runtime-PC-load-bias", func(t *testing.T) {
					resolver := NewUsymResolver()
					const pid = uint32(1001)
					key := cacheKey{inode: 1}
					resolver.processes[pid] = processELF{
						cacheKey: key,
						path:     path,
					}
					resolver.exeCache[key] = &executableCache{
						typ:     file.Type,
						symbols: elfSymbolCache{loadSegments: elfmapping.LoadSegments(file)},
					}
					for _, line := range lines[1:] {
						fields := strings.Fields(line)
						if len(fields) < 5 {
							continue
						}
						startText, endText, _ := strings.Cut(fields[0], "-")
						start, _ := strconv.ParseUint(startText, 16, 64)
						end, _ := strconv.ParseUint(endText, 16, 64)
						if runtimePC < start || runtimePC >= end {
							continue
						}
						offset, err := strconv.ParseInt(fields[2], 16, 64)
						if err != nil {
							t.Fatal(err)
						}
						resolver.procmaps[pid] = sections{&procfs.ProcMap{
							StartAddr: uintptr(start),
							EndAddr:   uintptr(end),
							Offset:    offset,
							Pathname:  path,
						}}
						break
					}
					if len(resolver.procmaps[pid]) == 0 {
						t.Fatal("runtime PC has no mapping")
					}
					for range 2 {
						if got := resolver.resolveAddr(pid, runtimePC); got != "main.main" {
							t.Fatalf("runtime PC %#x resolved as %s", runtimePC, got)
						}
					}
				})
			}
			if mode == "exe" || mode == "pie" {
				t.Run("ELF-symbol-pclntab-bounds", func(t *testing.T) {
					for _, name := range []string{".gopclntab", ".data.rel.ro.gopclntab"} {
						if section := file.Section(name); section != nil {
							section.Name = ".renamed_go_pclntab"
							t.Cleanup(func() { section.Name = name })
						}
					}
					pcln, _, scannedBytes, err := readPCLN(t.Context(), file, maxGoSymbolBytes)
					if err != nil || scannedBytes == 0 {
						t.Fatalf("ELF symbol bounds not used or charged: bytes=%d err=%v", scannedBytes, err)
					}
					resolved, usage, err := readGoTable(t.Context(), file, DefaultELFSymbolLimits())
					if err != nil {
						t.Fatal(err)
					}
					if got := resolved.PCToFunc(pc); got == nil || got.Name != "main.main" {
						t.Fatalf("ELF symbol fallback did not resolve runtime PC %#x: %v", pc, got)
					}
					if usage.metadataBytes < scannedBytes+uint64(len(pcln)) {
						t.Fatal("ELF lookup and pclntab did not share the metadata budget")
					}
					symbols := file.SectionByType(elf.SHT_SYMTAB)
					budget := symbols.Size + file.Sections[symbols.Link].Size - 1
					if _, _, _, err := readPCLN(t.Context(), file, budget); err == nil || !strings.Contains(err.Error(), "read ELF symbols") {
						t.Fatalf("ELF budget failure was bypassed: %v", err)
					}
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					if _, _, _, err := readPCLN(ctx, file, maxGoSymbolBytes); !errors.Is(err, context.Canceled) {
						t.Fatalf("ELF lookup cancellation was bypassed: %v", err)
					}
				})
			}
			t.Run("moduledata-text-base", func(t *testing.T) {
				pcln, address, _, err := readPCLN(t.Context(), file, maxGoSymbolBytes)
				if err != nil {
					t.Fatal(err)
				}
				base, _, err := goTextStart(t.Context(), file, pcln, address, maxGoSymbolBytes)
				if err != nil {
					t.Fatal(err)
				}
				pcln = bytes.Clone(pcln)
				clear(pcln[8+2*int(pcln[7]) : 8+3*int(pcln[7])])
				recovered, _, err := goTextStart(t.Context(), file, pcln, address, maxGoSymbolBytes)
				if err != nil || recovered != base {
					t.Fatalf("moduledata base=%#x, want %#x: %v", recovered, base, err)
				}
				if _, _, err := goTextStart(t.Context(), file, pcln, address, 0); !errors.Is(err, errELFSymbolLimit) {
					t.Fatalf("moduledata scan ignored metadata budget: %v", err)
				}
				// An unavailable module must not silently fall back to ELF .text.
				for _, section := range file.Sections {
					if section.Name == ".go.module" || section.Name == ".noptrdata" {
						name := section.Name
						section.Name = "hidden-moduledata"
						t.Cleanup(func() { section.Name = name })
					}
				}
				if _, _, err := goTextStart(t.Context(), file, pcln, address, maxGoSymbolBytes); err == nil {
					t.Fatal("guessed text base without pcHeader or moduledata")
				}
			})
			resolver := NewUsymResolver()
			cache := &elfSymbolCache{}
			pcs := []uint64{fn.Entry, fn.Entry}
			for range 2 {
				names, err := resolver.resolveELFPCs(path, cache, pcs)
				if err != nil || names[fn.Entry] != "main.main" {
					t.Fatalf("resolve Go PCs: names=%v err=%v", names, err)
				}
			}
			t.Run("zero-name-length", func(t *testing.T) {
				limits := DefaultELFSymbolLimits()
				limits.MaxNameLength = 0
				unlimited := NewUsymResolver(WithELFSymbolLimits(limits))
				names, err := unlimited.resolveELFPCs(path, &elfSymbolCache{}, pcs)
				if err != nil || names[fn.Entry] != "main.main" {
					t.Fatalf("zero MaxNameLength must resolve Go PCs: names=%v err=%v", names, err)
				}
			})
			if mode == "stripped" || mode == "stripped-pie" {
				if cache.goTable == nil || len(cache.namesByELFPC) != 1 {
					t.Fatal("Go fallback table or deduplicated PC was not cached")
				}
				if cache.state.metadataBytes > resolver.elfSymbolLimits.MaxMetadataBytes ||
					cache.state.nameBytes > resolver.elfSymbolLimits.MaxNameBytes ||
					cache.state.symbolCount > resolver.elfSymbolLimits.MaxSymbolAndCacheCount {
					t.Fatal("Go fallback exceeded shared ELF budgets")
				}
				for _, resource := range []string{"metadata", "count", "names", "length"} {
					t.Run(resource, func(t *testing.T) {
						limits := DefaultELFSymbolLimits()
						switch resource {
						case "metadata":
							limits.MaxMetadataBytes = 0
						case "count":
							limits.MaxSymbolAndCacheCount = 0
						case "names":
							limits.MaxNameBytes = 0
						case "length":
							limits.MaxNameLength = 1
						}
						bounded := NewUsymResolver(WithELFSymbolLimits(limits))
						failed := &elfSymbolCache{}
						names, err := bounded.resolveELFPCs(path, failed, pcs)
						if !errors.Is(err, errELFSymbolLimit) || len(names) != 0 || failed.goTable != nil {
							t.Fatalf("Go budget not enforced: names=%v err=%v", names, err)
						}
						_, again := bounded.resolveELFPCs(path, failed, pcs)
						if !errors.Is(again, errELFSymbolLimit) || !failed.goTableChecked {
							t.Fatalf("Go budget failure was not retained: %v", again)
						}
					})
				}
			}
			if _, err := file.Section(".text").Data(); err != nil {
				t.Fatalf("caller file closed: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := ReadGoTable(ctx, file); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if mode != "exe" || build.link != "internal" {
				return
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			section := file.Section(".gopclntab")
			for _, test := range []struct {
				name  string
				patch func([]byte)
			}{
				{"magic", func(p []byte) { p[0] = 0 }},
				{"count", func(p []byte) { file.ByteOrder.PutUint64(p[8:], maxGoSymbolEntries+1) }},
				{"offset", func(p []byte) { file.ByteOrder.PutUint64(p[32:], uint64(len(raw))) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					data := bytes.Clone(raw)
					test.patch(data[section.Offset:])
					corrupt, err := elf.NewFile(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					defer corrupt.Close()
					if _, err := ReadGoTable(t.Context(), corrupt); err == nil {
						t.Fatal("accepted corrupt pclntab")
					}
				})
			}
			section.Size = maxGoSymbolBytes
			if _, err := ReadGoTable(t.Context(), file); err == nil {
				t.Fatal("accepted oversized pclntab")
			}
		})
	}
}

func testGoPCLN(function string) []byte {
	const headerSize = 72
	const fileName = "main.go\x00"
	fileOffset := headerSize + len(function) + 1
	funcOffset := fileOffset + len(fileName)
	data := make([]byte, funcOffset+28)
	order := binary.LittleEndian
	order.PutUint32(data, 0xfffffff1)
	data[6], data[7] = 1, 8
	for index, value := range []uint64{1, 1, 0x400000, headerSize, uint64(fileOffset), uint64(fileOffset), uint64(funcOffset), uint64(funcOffset)} {
		order.PutUint64(data[8+index*8:], value)
	}
	copy(data[headerSize:], function)
	copy(data[fileOffset:], fileName)
	order.PutUint32(data[funcOffset+4:], 12)
	return data
}

func TestMergedPCLNCandidates(t *testing.T) {
	file := &elf.File{FileHeader: elf.FileHeader{ByteOrder: binary.LittleEndian}}
	pcln := testGoPCLN("main.hot")
	padding := make([]byte, 9*8)
	for offset := 0; offset < len(padding); offset += 8 {
		binary.LittleEndian.PutUint32(padding[offset:], 0xfffffff1)
	}
	data := append(bytes.Clone(padding), pcln...)
	offset, err := findMergedPCLN(t.Context(), file, data)
	if err != nil || offset != len(padding) {
		t.Fatalf("ordinary magic values counted as headers: offset=%d err=%v", offset, err)
	}
	bad := bytes.Clone(pcln)
	funcOffset := goPCLNWord(bad, 7, file)
	binary.LittleEndian.PutUint32(bad[funcOffset+12+4:], ^uint32(0))
	for len(bad)%4 != 0 {
		bad = append(bad, 0)
	}
	if _, err := findMergedPCLN(t.Context(), file, bytes.Repeat(bad, 9)); err == nil || !strings.Contains(err.Error(), "candidate limit exceeded") {
		t.Fatalf("expensive candidate parsing is not bounded: %v", err)
	}
	valid := bytes.Clone(pcln)
	for len(valid)%4 != 0 {
		valid = append(valid, 0)
	}
	if _, err := findMergedPCLN(t.Context(), file, bytes.Repeat(valid, 2)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("multiple valid tables were accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := findMergedPCLN(ctx, file, data); !errors.Is(err, context.Canceled) {
		t.Fatalf("candidate scan ignored cancellation: %v", err)
	}
}

func TestReadGoTableWithMagicTypeSizes(t *testing.T) {
	requireExternalGoCompiler(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	path := filepath.Join(dir, "fixture")
	var fixture strings.Builder
	fixture.WriteString("package main\nimport (\"fmt\"; \"reflect\")\n")
	for index := range 9 {
		fixture.WriteString("type T" + strconv.Itoa(index) + " [0xfffffff1]byte\n")
	}
	fixture.WriteString("func main() {\n")
	for index := range 9 {
		fixture.WriteString("fmt.Println(reflect.TypeOf((*T" + strconv.Itoa(index) + ")(nil)).Elem().Size())\n")
	}
	fixture.WriteString("}\n")
	if err := os.WriteFile(source, []byte(fixture.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-buildmode=pie", "-ldflags=-linkmode=external -s -w", "-o", path, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	// Reflect on nil array pointers: retain the type metadata without allocating
	// the large arrays whose sizes happen to equal pclntab magic.
	if out, err := exec.Command(path).CombinedOutput(); err != nil {
		t.Fatalf("run: %v: %s", err, out)
	}
	file, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	table, err := ReadGoTable(t.Context(), file)
	if err != nil || table.LookupFunc("main.main") == nil {
		t.Fatalf("valid Go program with magic type sizes was rejected: %v", err)
	}
}

func TestGoSymbolBudget(t *testing.T) {
	for _, counts := range [][3]uint64{{maxGoSymbolBytes, 0, 0}, {0, ^uint64(0), 0}, {0, 0, ^uint64(0)}} {
		if _, err := goSymbolBudget(counts[0], counts[1], counts[2]); err == nil {
			t.Fatalf("accepted budget: %v", counts)
		}
	}

	for _, tc := range []struct {
		name       string
		function   string
		nameLength uint64
		nameBudget uint64
		wantLimit  bool
	}{
		{
			name:       "zero",
			function:   "main.hot",
			nameBudget: maxGoSymbolNames,
		},
		{
			name:       "exact",
			function:   "main.hot",
			nameLength: 8,
			nameBudget: maxGoSymbolNames,
		},
		{
			name:       "above",
			function:   "main.hot",
			nameLength: 9,
			nameBudget: maxGoSymbolNames,
		},
		{
			name:       "below",
			function:   "main.hot",
			nameLength: 7,
			nameBudget: maxGoSymbolNames,
			wantLimit:  true,
		},
		{
			name:      "zero-total-budget",
			function:  "main.hot",
			wantLimit: true,
		},
		{
			name:       "zero-safety-cap",
			function:   string(bytes.Repeat([]byte{'x'}, maxGoSymbolName+1)),
			nameBudget: maxGoSymbolNames,
			wantLimit:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A minimal supported pclntab isolates name boundaries from unrelated
			// runtime names in a compiler-produced binary.
			data := testGoPCLN(tc.function)
			order := binary.LittleEndian
			limits := DefaultELFSymbolLimits()
			limits.MaxNameLength = tc.nameLength
			limits.MaxNameBytes = tc.nameBudget
			var usage goSymbolUsage
			err := validatePCLN(t.Context(), data, order, limits, &usage)
			if tc.wantLimit {
				if !errors.Is(err, errELFSymbolLimit) {
					t.Fatalf("expected name limit error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid name boundary rejected: %v", err)
			}
		})
	}
}

func BenchmarkReadGoTable(b *testing.B) {
	dir := b.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	for _, link := range []string{"internal", "external"} {
		b.Run(link, func(b *testing.B) {
			if link == "external" {
				requireExternalGoCompiler(b)
			}
			path := filepath.Join(dir, link)
			command := exec.Command("go", "build", "-buildmode=pie", "-ldflags=-linkmode="+link+" -s -w", "-o", path, source)
			cgo := "0"
			if link == "external" {
				cgo = "1"
			}
			command.Env = append(os.Environ(), "CGO_ENABLED="+cgo)
			if out, err := command.CombinedOutput(); err != nil {
				b.Fatalf("build: %v: %s", err, out)
			}
			file, err := elf.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := ReadGoTable(b.Context(), file); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
