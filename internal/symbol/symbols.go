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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
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

// elfSymbols extracts all STT_FUNC entries from .dynsym and .symtab.
func elfSymbols(f *elf.File) symbols {
	syms := symbols{}
	type section struct {
		name  string
		fetch func() ([]elf.Symbol, error)
	}
	for _, s := range []section{{"dynsym", f.DynamicSymbols}, {"symtab", f.Symbols}} {
		elfsyms, err := s.fetch()
		if err != nil {
			log.Infof("symbol: %s not available in %s: %v", s.name, f.FileHeader.Type, err)
			continue
		}
		before := len(syms)
		for _, sym := range elfsyms {
			if elf.ST_TYPE(sym.Info) == elf.STT_FUNC {
				syms = append(syms, &symbol{Addr: sym.Value, Size: sym.Size, Name: sym.Name})
			}
		}
		log.Infof("symbol: %s extracted %d func symbols", s.name, len(syms)-before)
	}
	syms.sort()
	return syms
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
	mounts = xfsMounts
	mountsInited = true
	log.Infof("symbol: discovered %d xfs mount(s): %v", len(mounts), mounts)
	return nil
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

// FormatStack formats a stack trace (newline-separated addresses)
// into a readable form with frame indices.
// Returns nil if stack is empty.
func FormatStack(stack string) []string {
	if stack == "" {
		return nil
	}

	var lines []string
	for _, frame := range strings.Split(strings.TrimRight(stack, "\n"), "\n") {
		if frame != "" {
			lines = append(lines, frame)
		}
	}
	return lines
}

// FormatStackLines formats stack lines for output with indices.
func FormatStackLines(w io.Writer, stack string) error {
	lines := FormatStack(stack)
	for i, frame := range lines {
		if _, err := fmt.Fprintf(w, "\t#%-2d  %s\n", i, frame); err != nil {
			return err
		}
	}
	return nil
}
