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
	"bufio"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/profiler/procutil"
)

type outType uint8

const (
	outTypeString outType = iota
	outTypeBytes
)

type stackFrames struct {
	strings []string
	bytes   [][]byte
}

// ErrELFSymbolLimit reports that an ELF symbol source exceeded its configured budget.
var ErrELFSymbolLimit = errors.New("ELF symbol resource limit exceeded")

// ELFSymbolLimits bounds the metadata materialized while parsing one ELF.
type ELFSymbolLimits struct {
	MaxMetadataBytes uint64
	MaxSymbolCount   uint64
	MaxNameBytes     uint64
	MaxNameLength    uint64
}

// DefaultELFSymbolLimits returns the default per-ELF symbol parsing limits.
func DefaultELFSymbolLimits() ELFSymbolLimits {
	return ELFSymbolLimits{
		MaxMetadataBytes: 16 << 20,
		MaxSymbolCount:   500_000,
		MaxNameBytes:     16 << 20,
		MaxNameLength:    1 << 20,
	}
}

// symbol is the unified symbol descriptor shared by all resolvers.
type symbol struct {
	Addr   uint64
	Size   uint64 // 0 = unknown (kernel symbols)
	Name   string
	Module string
}

func (s symbol) String() string {
	return fmt.Sprintf("{Addr: %x Name: %s Module: %s}", s.Addr, s.Name, s.Module)
}

// symbols is a symbol slice sorted by Addr ascending.
type (
	symbols  []*symbol
	sections []*procfs.ProcMap
)

var (
	// mounts caches the discovered XFS mount points used to disambiguate
	// inode-only cache keys in multi-mount container environments.
	mounts = []string{}
	// mountsInited reports whether mounts has already been populated.
	mountsInited bool
)

func (syms symbols) sort() {
	sort.Slice(syms, func(i, j int) bool { return syms[i].Addr < syms[j].Addr })
}

// failFrame returns a diagnostic frame string for unresolvable addresses.
// Format: "[unknown reason {path}]"
func failFrame(reason, path string) string {
	if reason == "" && path == "" {
		return "unknown"
	}

	return "unknown " + reason + path
}

// resolve returns the symbol name covering key, or empty string.
// Symbols with Size==0 (kernel-style) accept any key >= Addr.
func (syms symbols) resolve(key uint64) string {
	sym := syms.floorSym(key)
	if sym == nil || sym.Name == "" {
		return ""
	}
	if sym.Size == 0 || key < sym.Addr+sym.Size {
		return sym.Name
	}
	return ""
}

func (secs sections) sort() {
	sort.Slice(secs, func(i, j int) bool { return secs[i].StartAddr < secs[j].StartAddr })
}

// findBaseAddr returns the load base address for the named library.
// It locates the first mapping (lowest StartAddr) and subtracts its file
// offset so that ELF virtual addresses can be compared directly.
func (secs sections) findBaseAddr(pathname string) (uint64, bool) {
	for _, s := range secs {
		if s.Pathname == pathname {
			return uint64(s.StartAddr) - uint64(s.Offset), true
		}
	}
	return 0, false
}

// find returns the section containing addr from a start-sorted slice, or nil.
func (secs sections) find(addr uint64) *procfs.ProcMap {
	idx := searchFloorIndex(len(secs), func(i int) bool { return uint64(secs[i].StartAddr) > addr })
	if idx < 0 || idx >= len(secs) {
		return nil
	}
	if s := secs[idx]; s.Pathname != "" && addr >= uint64(s.StartAddr) && addr < uint64(s.EndAddr) {
		return s
	}
	return nil
}

// resolveStack resolves frames in forward order over the valid stack prefix
// ([0:firstZero], or full slice if no zero terminator exists).
func resolveStack(stack []uint64, resolve func(addr uint64) string, out ...outType) stackFrames {
	mode := outTypeString
	if len(out) > 0 {
		mode = out[0]
	}
	frames := stackFrames{
		strings: []string{},
		bytes:   [][]byte{},
	}
	if mode == outTypeBytes {
		frames.bytes = make([][]byte, 0, len(stack))
	} else {
		frames.strings = make([]string, 0, len(stack))
	}
	valid := len(stack)
	for i, addr := range stack {
		if addr == 0 {
			valid = i
			break
		}
	}

	for i := 0; i < valid; i++ {
		addr := stack[i]
		name := resolve(addr)
		if name == "" {
			name = failFrame("", "")
		}
		if mode == outTypeBytes {
			frames.bytes = append(frames.bytes, []byte(name))
		} else {
			frames.strings = append(frames.strings, name)
		}
	}
	return frames
}

// searchFloorIndex returns the index of the largest item that is <= key.
// The callback should return true when item[index] > key.
func searchFloorIndex(n int, isGreater func(index int) bool) int {
	if n == 0 {
		return -1
	}
	return sort.Search(n, isGreater) - 1
}

// floorSym returns the last symbol with Addr <= key, or nil.
func (syms symbols) floorSym(key uint64) *symbol {
	idx := searchFloorIndex(len(syms), func(i int) bool { return syms[i].Addr > key })
	if idx < 0 || idx >= len(syms) {
		return nil
	}
	return syms[idx]
}

// parseKallsymsLine parses one /proc/kallsyms line into a symbol.
// It returns a zero symbol and false if the line is not a text symbol.
func parseKallsymsLine(line string) (*symbol, bool) {
	words := strings.Fields(line)
	if len(words) != 3 && len(words) != 4 {
		return &symbol{}, false
	}
	if words[1] != "T" && words[1] != "t" && words[1] != "R" {
		return &symbol{}, false
	}

	addr, err := strconv.ParseUint(words[0], 16, 64)
	if err != nil {
		return &symbol{}, false
	}
	module := "[kernel]"
	if len(words) == 4 {
		module = words[3]
	}
	return &symbol{Addr: addr, Name: words[2], Module: module}, true
}

// scanKallsyms reads path and returns all text symbols as an unsorted symbols.
func scanKallsyms(path string, capacity int) (symbols, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	syms := make(symbols, 0, capacity)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if sym, ok := parseKallsymsLine(scanner.Text()); ok {
			syms = append(syms, sym)
		}
	}
	return syms, scanner.Err()
}

type elfSymbolSource struct {
	name string
	typ  elf.SectionType
}

type elfSymbolParseState struct {
	limits           ELFSymbolLimits
	metadataBytes    uint64
	symbolCount      uint64
	nameBytes        uint64
	metadataSections map[*elf.Section]struct{}
	names            map[*elf.Section]map[uint32]string
}

func newELFSymbolParseState(limits ELFSymbolLimits) *elfSymbolParseState {
	return &elfSymbolParseState{
		limits:           limits,
		metadataSections: make(map[*elf.Section]struct{}),
		names:            make(map[*elf.Section]map[uint32]string),
	}
}

func elfSymbolSourceSections(f *elf.File, source elfSymbolSource) (*elf.Section, *elf.Section, error) {
	section := f.SectionByType(source.typ)
	if section == nil {
		return nil, nil, elf.ErrNoSymbols
	}
	if section.Link == 0 || section.Link >= uint32(len(f.Sections)) {
		return nil, nil, fmt.Errorf("symbol section has invalid string table link %d", section.Link)
	}
	stringsSection := f.Sections[section.Link]
	if stringsSection.Type != elf.SHT_STRTAB {
		return nil, nil, fmt.Errorf("symbol section links to %s instead of SHT_STRTAB", stringsSection.Type)
	}
	return section, stringsSection, nil
}

func (state *elfSymbolParseState) parseSource(f *elf.File, source elfSymbolSource, pcs ...uint64) (symbols, error) {
	section, stringsSection, err := elfSymbolSourceSections(f, source)
	if err != nil {
		return nil, err
	}

	var symbolSize uint64
	switch f.Class {
	case elf.ELFCLASS32:
		symbolSize = elf.Sym32Size
	case elf.ELFCLASS64:
		symbolSize = elf.Sym64Size
	default:
		return nil, fmt.Errorf("unsupported ELF class %s", f.Class)
	}

	if section.Size == 0 || section.Size%symbolSize != 0 {
		return nil, fmt.Errorf("symbol section size %d is not a non-zero multiple of %d", section.Size, symbolSize)
	}
	// The first symbol is the required all-zero entry and is not returned.
	symbolCount := section.Size/symbolSize - 1
	remainingSymbols := state.limits.MaxSymbolCount - state.symbolCount
	if symbolCount > remainingSymbols {
		return nil, fmt.Errorf("%w: %d symbols exceed the remaining limit of %d", ErrELFSymbolLimit, symbolCount, remainingSymbols)
	}

	var metadataBytes uint64
	for _, candidate := range []*elf.Section{section, stringsSection} {
		if _, loaded := state.metadataSections[candidate]; loaded {
			continue
		}
		remainingMetadata := state.limits.MaxMetadataBytes - state.metadataBytes - metadataBytes
		if candidate.Size > remainingMetadata {
			return nil, fmt.Errorf("%w: metadata exceeds the remaining %d-byte limit", ErrELFSymbolLimit, remainingMetadata)
		}
		metadataBytes += candidate.Size
	}

	// Do not use File.Symbols or File.DynamicSymbols here. debug/elf expands
	// every symbol name before callers can filter by type, so repeated offsets
	// can multiply a bounded string table into unbounded allocations.
	data, err := section.Data()
	if err != nil {
		return nil, fmt.Errorf("load symbol section: %w", err)
	}
	if uint64(len(data)) != section.Size {
		return nil, fmt.Errorf("ELF section data size differs from its declared size")
	}

	sharedNames := state.names[stringsSection]
	localNames := make(map[uint32]string)
	var nameBytes uint64
	type candidate struct {
		nameOffset uint32
		value      uint64
		size       uint64
	}
	candidates := make(map[uint64]candidate, len(pcs))
	allSymbols := len(pcs) == 0
	result := make(symbols, 0, len(pcs))
	for offset := symbolSize; offset < uint64(len(data)); offset += symbolSize {
		entry := data[offset : offset+symbolSize]
		var nameOffset uint32
		var info byte
		var value, size uint64
		if f.Class == elf.ELFCLASS32 {
			nameOffset = f.ByteOrder.Uint32(entry[0:4])
			value = uint64(f.ByteOrder.Uint32(entry[4:8]))
			size = uint64(f.ByteOrder.Uint32(entry[8:12]))
			info = entry[12]
		} else {
			nameOffset = f.ByteOrder.Uint32(entry[0:4])
			info = entry[4]
			value = f.ByteOrder.Uint64(entry[8:16])
			size = f.ByteOrder.Uint64(entry[16:24])
		}
		if elf.ST_TYPE(info) != elf.STT_FUNC {
			continue
		}
		if !allSymbols {
			for _, pc := range pcs {
				if value <= pc {
					if current, ok := candidates[pc]; !ok || value > current.value {
						candidates[pc] = candidate{nameOffset: nameOffset, value: value, size: size}
					}
				}
			}
			continue
		}
		candidates[uint64(len(candidates))] = candidate{nameOffset: nameOffset, value: value, size: size}
	}

	for pc, matched := range candidates {
		if !allSymbols && matched.size != 0 && pc >= matched.value+matched.size {
			// Keep the unnamed floor candidate so a lower symbol from another
			// table cannot incorrectly resolve this PC.
			result = append(result, &symbol{Addr: matched.value, Size: matched.size})
			continue
		}
		name, ok := sharedNames[matched.nameOffset]
		if !ok {
			name, ok = localNames[matched.nameOffset]
		}
		if !ok {
			if uint64(matched.nameOffset) >= stringsSection.Size {
				return nil, fmt.Errorf("symbol name offset %d exceeds string table size %d", matched.nameOffset, stringsSection.Size)
			}
			remainingNames := state.limits.MaxNameBytes - state.nameBytes - nameBytes
			maxNameLength := state.limits.MaxNameLength
			if maxNameLength == 0 || maxNameLength > remainingNames {
				maxNameLength = remainingNames
			}
			name, err = readELFSymbolName(stringsSection, matched.nameOffset, maxNameLength)
			if err != nil {
				return nil, err
			}
			localNames[matched.nameOffset] = name
			nameBytes += uint64(len(name))
		}
		result = append(result, &symbol{Addr: matched.value, Size: matched.size, Name: name})
	}

	state.metadataBytes += metadataBytes
	state.symbolCount += symbolCount
	state.nameBytes += nameBytes
	state.metadataSections[section] = struct{}{}
	state.metadataSections[stringsSection] = struct{}{}
	if sharedNames == nil {
		sharedNames = make(map[uint32]string, len(localNames))
		state.names[stringsSection] = sharedNames
	}
	for offset, name := range localNames {
		sharedNames[offset] = name
	}
	return result, nil
}

func readELFSymbolName(section *elf.Section, offset uint32, limit uint64) (string, error) {
	if limit >= uint64(^uint64(0)>>1) {
		return "", fmt.Errorf("%w: symbol name limit is too large", ErrELFSymbolLimit)
	}
	reader := section.Open()
	if _, err := reader.Seek(int64(offset), io.SeekStart); err != nil {
		return "", fmt.Errorf("seek symbol name at offset %d: %w", offset, err)
	}
	available := section.Size - uint64(offset)
	readLimit := min(available, limit+1)
	bufferSize := int(min(readLimit, 4096))
	name, err := bufio.NewReaderSize(io.LimitReader(reader, int64(readLimit)), bufferSize).ReadBytes(0)
	if len(name) > 0 && name[len(name)-1] == 0 {
		return string(name[:len(name)-1]), nil
	}
	if uint64(len(name)) > limit {
		return "", fmt.Errorf("%w: symbol name at offset %d exceeds the %d-byte limit", ErrELFSymbolLimit, offset, limit)
	}
	return "", fmt.Errorf("symbol name at offset %d is not NUL-terminated: %w", offset, err)
}

func elfSymbolsFromSources(f *elf.File, sources []elfSymbolSource, limits ELFSymbolLimits) (symbols, error) {
	syms := symbols{}
	state := newELFSymbolParseState(limits)
	var limitErrors []error
	for _, source := range sources {
		sourceSymbols, err := state.parseSource(f, source)
		if err != nil {
			if errors.Is(err, ErrELFSymbolLimit) {
				limitErrors = append(limitErrors, fmt.Errorf("%s: %w", source.name, err))
			} else {
				log.Infof("symbol: %s not available in %s: %v", source.name, f.FileHeader.Type, err)
			}
			continue
		}
		syms = append(syms, sourceSymbols...)
		log.Infof("symbol: %s extracted %d func symbols", source.name, len(sourceSymbols))
	}
	syms.sort()
	return syms, errors.Join(limitErrors...)
}

// elfSymbols extracts all STT_FUNC entries from .dynsym and .symtab. Version
// metadata is intentionally not parsed because the resolver only consumes the
// symbol name, address, and size.
func elfSymbols(f *elf.File, limits ELFSymbolLimits) (symbols, error) {
	return elfSymbolsFromSources(f, []elfSymbolSource{
		{name: "dynsym", typ: elf.SHT_DYNSYM},
		{name: "symtab", typ: elf.SHT_SYMTAB},
	}, limits)
}

// elfSymbolsForPCs scans bounded symbol metadata but materializes names only
// for the symbols that cover the requested ELF-relative PCs.
func elfSymbolsForPCs(f *elf.File, pcs []uint64, limits ELFSymbolLimits) (symbols, error) {
	syms := symbols{}
	state := newELFSymbolParseState(limits)
	var limitErrors []error
	for _, source := range []elfSymbolSource{{name: "dynsym", typ: elf.SHT_DYNSYM}, {name: "symtab", typ: elf.SHT_SYMTAB}} {
		sourceSymbols, err := state.parseSource(f, source, pcs...)
		if err != nil {
			if errors.Is(err, ErrELFSymbolLimit) {
				limitErrors = append(limitErrors, fmt.Errorf("%s: %w", source.name, err))
			} else {
				log.Infof("symbol: %s not available in %s: %v", source.name, f.FileHeader.Type, err)
			}
			continue
		}
		syms = append(syms, sourceSymbols...)
	}
	syms.sort()
	return syms, errors.Join(limitErrors...)
}

// backedPaths is the set of pseudo-paths in /proc/<pid>/maps with no ELF symbols.
var backedPaths = map[string]struct{}{
	"anon_inode:[perf_event]": {},
	"[stack]":                 {},
	"[vvar]":                  {},
	"[vdso]":                  {},
	"[vsyscall]":              {},
	"[heap]":                  {},
	"//anon":                  {},
	"/dev/zero":               {},
	"/anon_hugepage":          {},
	"/SYSV":                   {},
}

func isLibPath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	_, blocked := backedPaths[path]
	return !blocked
}

// parseMaps reads /proc/<pid>/maps and returns raw proc maps.
func parseMaps(pid uint32) (sections, error) {
	proc, err := procfs.NewProc(int(pid))
	if err != nil {
		return nil, err
	}
	return proc.ProcMaps()
}

func xfsMountPoints() ([]string, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	mountInfo, err := fs.GetMounts()
	if err != nil {
		return nil, err
	}

	xfsMounts := make([]string, 0, len(mountInfo))
	seen := make(map[string]struct{}, len(mountInfo))
	for _, mount := range mountInfo {
		if mount == nil || mount.FSType != "xfs" {
			continue
		}

		mountPoint := filepath.Clean(mount.MountPoint)
		if mountPoint == "" {
			continue
		}
		if _, ok := seen[mountPoint]; ok {
			continue
		}

		seen[mountPoint] = struct{}{}
		xfsMounts = append(xfsMounts, mountPoint)
	}
	return xfsMounts, nil
}

func initXfsMounts() error {
	if mountsInited {
		return nil
	}

	xfsMounts, err := xfsMountPoints()
	if err != nil {
		return err
	}

	if selfInContainer, _ := procutil.IsProcessInContainer(os.Getpid()); selfInContainer {
		hostMounts, err := xfsMountPointsFromHost()
		if err == nil && len(hostMounts) > 0 {
			xfsMounts = hostMounts
			log.Infof("symbol: discovered %d host xfs mount(s): %v", len(xfsMounts), xfsMounts)
		}
	}

	mounts = xfsMounts
	mountsInited = true
	log.Infof("symbol: discovered %d xfs mount(s): %v", len(mounts), mounts)
	return nil
}

func xfsMountPointsFromHost() ([]string, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	mountInfo, err := fs.GetProcMounts(1)
	if err != nil {
		return nil, err
	}

	xfsMounts := make([]string, 0, len(mountInfo))
	seen := make(map[string]struct{}, len(mountInfo))
	for _, mount := range mountInfo {
		if mount == nil || mount.FSType != "xfs" {
			continue
		}

		mountPoint := filepath.Clean(mount.MountPoint)
		if mountPoint == "" {
			continue
		}
		if _, ok := seen[mountPoint]; ok {
			continue
		}

		seen[mountPoint] = struct{}{}
		xfsMounts = append(xfsMounts, mountPoint)
	}
	return xfsMounts, nil
}

func countXfsMounts() (int, error) {
	if err := initXfsMounts(); err != nil {
		return 0, err
	}
	return len(mounts), nil
}

func matchXfsMount(path string, xfsMounts []string) (string, error) {
	for _, mount := range xfsMounts {
		if path == mount || strings.HasPrefix(path, strings.TrimRight(mount, "/")+"/") {
			return mount, nil
		}
	}

	return "", fmt.Errorf("no xfs mount found for path %q", path)
}

func lowerDirFromMountInfo(pid uint32) (string, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return "", err
	}

	mountInfo, err := fs.GetProcMounts(int(pid))
	if err != nil {
		return "", err
	}

	for _, mount := range mountInfo {
		if mount == nil || mount.FSType != "overlay" {
			continue
		}

		lowerDir, ok := mount.SuperOptions["lowerdir"]
		if !ok || lowerDir == "" {
			continue
		}

		dirs := strings.Split(lowerDir, ":")
		if len(dirs) == 0 {
			continue
		}
		return filepath.Clean(dirs[len(dirs)-1]), nil
	}

	return "", fmt.Errorf("lowerdir not found for pid %d", pid)
}

// FormatStackLines formats a stack trace (newline-separated addresses)
// and writes it to w with frame indices.
func FormatStackLines(w io.Writer, stack string) error {
	i := 0
	for _, frame := range strings.Split(strings.TrimRight(stack, "\n"), "\n") {
		if frame == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "\t#%-2d  %s\n", i, frame); err != nil {
			return err
		}
		i++
	}
	return nil
}
