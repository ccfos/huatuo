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

// Usym User mode stack information
type Usym struct {
	elfcaches map[uint32]elfcache
	libcaches map[string][]symbol
}

// NewUsym creates a new Usym object
func NewUsym() *Usym {
	return &Usym{
		elfcaches: make(map[uint32]elfcache),
		libcaches: make(map[string][]symbol),
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
