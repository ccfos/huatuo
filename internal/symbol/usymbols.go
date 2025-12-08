// Copyright 2025 The HuaTuo Authors
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
	"bufio"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/log"
)

const (
	// elftype elf type
	elftype int = 0xc
	// libtype lib type
	libtype int = 0xd
)

type symbol struct {
	name  string
	start uint64
	size  uint64
}

type section struct {
	name        string
	start       uint64
	end         uint64
	sectiontype int
}

// elfcache elf slice
type elfcache struct {
	sections  []section
	symcaches []symbol
}

// perfMapInfo stores perf map file information
type perfMapInfo struct {
	symbols  []symbol
	modTime  int64  // file modification time in Unix timestamp
	filePath string // actual file path being monitored
}

// Usym User mode stack information
type Usym struct {
	elfcaches    map[uint32]elfcache
	libcaches    map[string][]symbol
	perfMapCache map[uint32]*perfMapInfo // JIT symbol cache from perf-<pid>.map files
}

// NewUsym creates a new Usym object
func NewUsym() *Usym {
	return &Usym{
		elfcaches:    make(map[uint32]elfcache),
		libcaches:    make(map[string][]symbol),
		perfMapCache: make(map[uint32]*perfMapInfo),
	}
}

func (m *Usym) getElfSymbols(f *elf.File) []symbol {
	tabSym := []symbol{}
	dynsymbols, err := f.DynamicSymbols()
	if err != nil {
		log.Debugf("Usym elf no dynsymbols err %v", err)
	} else {
		for _, dsym := range dynsymbols {
			if elf.ST_TYPE(dsym.Info) == elf.STT_FUNC {
				tabSym = append(tabSym, symbol{name: dsym.Name, start: dsym.Value, size: dsym.Size})
			}
		}
	}

	symbols, err := f.Symbols()
	if err != nil {
		log.Debugf("Usym elf no symbols err %v", err)
	} else {
		for _, sym := range symbols {
			if elf.ST_TYPE(sym.Info) == elf.STT_FUNC {
				tabSym = append(tabSym, symbol{name: sym.Name, start: sym.Value, size: sym.Size})
			}
		}
	}
	return tabSym
}

var backedArr = []string{"anon_inode:[perf_event]", "[stack]", "[vvar]", "[vdso]", "[vsyscall]", "[heap]", "//anon", "/dev/zero", "/anon_hugepage", "/SYSV"}

func (m *Usym) isInBacked(str string) bool {
	for _, item := range backedArr {
		if item == str {
			return true
		}
	}
	return false
}

func (m *Usym) getExePath(pid uint32) (string, error) {
	progpath := fmt.Sprintf("/proc/%d/exe", pid)
	binpath, err := os.Readlink(progpath)
	if err != nil {
		return "", err
	}
	res := filepath.Join(fmt.Sprintf("/proc/%d/root", pid), binpath)
	log.Debugf("Usym path: %v", res)
	return res, nil
}

func (m *Usym) loadElfCaches(addr uint64, pid uint32) error {
	if _, ok := m.elfcaches[pid]; ok {
		return nil
	}
	// load sections
	sectionArray := []section{}

	path, err := m.getExePath(pid)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("exepath is nil")
	}

	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sections := f.Sections
	for _, s := range sections {
		sectionArray = append(sectionArray, section{name: s.Name, start: s.Addr, end: s.Addr + s.Size, sectiontype: elftype})
	}

	// load maps
	mapPath := fmt.Sprintf("/proc/%d/maps", pid)
	file, err := os.Open(mapPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if err = scanner.Err(); err != nil {
		return err
	}

	for scanner.Scan() {
		line := scanner.Text()
		field := strings.Fields(line)
		if len(field) < 6 {
			continue
		}
		addr := strings.Split(field[0], "-")
		start := addr[0]
		end := addr[1]
		path := field[5]

		startNum, _ := strconv.ParseUint(start, 16, 64)
		endNum, _ := strconv.ParseUint(end, 16, 64)
		if !m.isInBacked(path) {
			sectionArray = append(sectionArray, section{name: path, start: startNum, end: endNum, sectiontype: libtype})
		}
	}
	sort.Slice(sectionArray, func(i, j int) bool { return sectionArray[i].start < sectionArray[j].start })
	log.Debugf("Usym elf + maps section: %v", sectionArray)

	// load elfsymbols
	tabsymbol := m.getElfSymbols(f)
	sort.Slice(tabsymbol, func(i, j int) bool { return tabsymbol[i].start < tabsymbol[j].start })

	var elf elfcache
	elf.sections = sectionArray
	elf.symcaches = tabsymbol
	m.elfcaches[pid] = elf
	return nil
}

func (m *Usym) loadLibCache(libPath string) error {
	if _, ok := m.libcaches[libPath]; ok {
		return nil
	}
	f, err := elf.Open(libPath)
	if err != nil {
		return err
	}
	defer f.Close()
	mtabsymbols := m.getElfSymbols(f)
	sort.Slice(mtabsymbols, func(i, j int) bool { return mtabsymbols[i].start < mtabsymbols[j].start })
	m.libcaches[libPath] = mtabsymbols
	return nil
}

// loadPerfMapCache loads JIT symbols from /tmp/perf-<pid>.map file
// This file is generated by Node.js/V8 when started with --perf-basic-prof flag
// Format: <hex_start_addr> <hex_size> <symbol_name>
// Example: 3f2c8b83140 3a LazyCompile:~processTicksAndRejections internal/process/task_queues.js:65
//
// Note: In containerized environments, the PID in the filename (e.g., perf-7.map) is the
// container-namespace PID, which differs from the host PID we receive. So we need to
// scan all perf-*.map files in the container's /tmp directory.
//
// The perf map file is continuously updated by V8/JIT as new functions are compiled,
// so we check the modification time and reload if the file has been updated.
func (m *Usym) loadPerfMapCache(pid uint32) error {
	// Check if we need to reload the perf map file
	if info, ok := m.perfMapCache[pid]; ok {
		// Check if the file has been modified since last load
		if stat, err := os.Stat(info.filePath); err == nil {
			modTime := stat.ModTime().Unix()
			if modTime == info.modTime {
				// File hasn't changed, use cached symbols
				return nil
			}
			log.Debugf("Usym perf map file %s has been updated (old: %d, new: %d), reloading", info.filePath, info.modTime, modTime)
		}
	}

	// Try to find perf map files in the container's /tmp directory
	// The container's root filesystem is accessible via /proc/<pid>/root
	containerTmpPath := fmt.Sprintf("/proc/%d/root/tmp", pid)

	// First, try to scan all perf-*.map files in the container's /tmp
	entries, err := os.ReadDir(containerTmpPath)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Match perf-<number>.map pattern
			if strings.HasPrefix(name, "perf-") && strings.HasSuffix(name, ".map") {
				mapPath := filepath.Join(containerTmpPath, name)
				// Parse the perf map file even if it's empty initially
				// V8/JIT will continuously append new symbols to this file
				if symbols, modTime, err := m.parsePerfMapFile(mapPath); err == nil {
					m.perfMapCache[pid] = &perfMapInfo{
						symbols:  symbols,
						modTime:  modTime,
						filePath: mapPath,
					}
					log.Debugf("Usym loaded %d JIT symbols from %s for host pid %d", len(symbols), mapPath, pid)
					return nil
				}
			}
		}
	}

	// Fallback: try the exact pid match (for non-containerized processes)
	fallbackPaths := []string{
		fmt.Sprintf("/proc/%d/root/tmp/perf-%d.map", pid, pid),
		fmt.Sprintf("/tmp/perf-%d.map", pid),
	}

	for _, path := range fallbackPaths {
		if symbols, modTime, err := m.parsePerfMapFile(path); err == nil {
			m.perfMapCache[pid] = &perfMapInfo{
				symbols:  symbols,
				modTime:  modTime,
				filePath: path,
			}
			log.Debugf("Usym loaded %d JIT symbols from %s for pid %d", len(symbols), path, pid)
			return nil
		}
	}

	log.Debugf("Usym no perf map file found for pid %d", pid)
	return fmt.Errorf("perf map file not found for pid %d", pid)
}

// parsePerfMapFile parses a perf map file and returns the symbols and file modification time
func (m *Usym) parsePerfMapFile(path string) ([]symbol, int64, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	modTime := stat.ModTime().Unix()

	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	var symbols []symbol
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: <hex_addr> <hex_size> <name>
		// Example: 3f2c8b83140 3a LazyCompile:~foo script.js:10
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}

		startAddr, err := strconv.ParseUint(parts[0], 16, 64)
		if err != nil {
			continue
		}
		size, err := strconv.ParseUint(parts[1], 16, 64)
		if err != nil {
			continue
		}
		name := parts[2]

		symbols = append(symbols, symbol{
			name:  name,
			start: startAddr,
			size:  size,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}

	sort.Slice(symbols, func(i, j int) bool { return symbols[i].start < symbols[j].start })
	return symbols, modTime, nil
}

func (m *Usym) searchSection(pid uint32, addr uint64) *section {
	if _, ok := m.elfcaches[pid]; !ok {
		return &section{}
	}
	progsection := m.elfcaches[pid].sections
	index := sort.Search(len(progsection), func(i int) bool {
		return progsection[i].start > addr
	})
	if index == len(progsection) {
		return &section{}
	}
	index--
	log.Debugf("Usym searchSection addr %d index %v len %v", addr, index, len(progsection))
	if index < len(progsection) && index >= 0 {
		log.Debugf("Usym searchSection curr %v next %v", progsection[index], progsection[index+1])
		start := progsection[index].start
		end := progsection[index].end
		if progsection[index].name != "" && addr <= end && addr >= start {
			return &progsection[index]
		}
		return &section{}
	}
	return &section{}
}

func (m *Usym) searchSym(addr uint64, symbols []symbol) string {
	index := sort.Search(len(symbols), func(i int) bool {
		return symbols[i].start > addr
	})
	if index == len(symbols) {
		return ""
	}
	index--
	log.Debugf("Usym searchSym addr %d index %v len %v", addr, index, len(symbols))
	if index < len(symbols) && index >= 0 {
		log.Debugf("Usym searchSym curr %v next %v", symbols[index], symbols[index+1])
		start := symbols[index].start
		size := symbols[index].size
		if symbols[index].name != "" && addr >= start && addr < start+size {
			return symbols[index].name
		}
		return "<unknown>"
	}
	return ""
}

// ResolveUstack display user mode stack information
func (m *Usym) ResolveUstack(addr uint64, pid uint32) string {
	log.Debugf("Usym ResolveUstack addr %d pid %d", addr, pid)

	// First, try to resolve from JIT perf map (Node.js/V8, Java, etc.)
	// This should be checked first because JIT code addresses might not be in ELF sections
	// However, we only return early if we find a valid symbol name (not <unknown>)
	if err := m.loadPerfMapCache(pid); err == nil {
		if info, ok := m.perfMapCache[pid]; ok && len(info.symbols) > 0 {
			if name := m.searchSym(addr, info.symbols); name != "" && name != "<unknown>" {
				log.Debugf("Usym resolved from perf map: %s", name)
				return name
			}
			// If we get <unknown> or "", continue to try ELF symbols
			// because the address might be in native code (Node.js C++ internals, libc, etc.)
			log.Debugf("Usym perf map lookup failed, trying ELF symbols")
		}
	}

	err := m.loadElfCaches(addr, pid)
	if err != nil {
		log.Debugf("Usym loadElfCaches err %v", err)
		return ""
	}
	// search elf section
	sec := m.searchSection(pid, addr)
	if sec.name == "" {
		return ""
	}
	// search elf symbol
	if sec.sectiontype == elftype {
		if _, ok := m.elfcaches[pid]; !ok {
			return ""
		}
		log.Debugf("Usym elf type")
		return m.searchSym(addr, m.elfcaches[pid].symcaches)
	}
	// search lib symbol
	libpath := filepath.Join(fmt.Sprintf("/proc/%d/root", pid), sec.name)
	baseaddr := sec.start
	addr -= baseaddr
	err = m.loadLibCache(libpath)
	if err != nil {
		log.Debugf("Usym loadLibCache err %v", err)
		return ""
	}
	if _, ok := m.libcaches[libpath]; !ok {
		return ""
	}
	log.Debugf("Usym lib type libpath %v", libpath)
	return m.searchSym(addr, m.libcaches[libpath])
}
