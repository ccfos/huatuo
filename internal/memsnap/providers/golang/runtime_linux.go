// Copyright 2022-2025 The Parca Authors
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
//
// This file contains work derived from github.com/parca-dev/oomprof.
// It was modified by The HuaTuo Authors for integration with HuaTuo.

package golang

import (
	"bufio"
	"bytes"
	"context"
	"debug/elf"
	"debug/gosym"
	"encoding/binary"
	"errors"
	"fmt"
	versionpkg "go/version"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	defaultProcRoot     = "/proc"
	minGoVersion        = "go1.18"
	maxGoVersion        = "go1.26"
	maxELFMetadataBytes = 64 << 20
	maxELFSymbols       = 1 << 20
)

var errMBucketsSymbolNotFound = errors.New("runtime.mbuckets symbol not found")

type imageInfo struct {
	goVersion   string
	mbucketsSym uint64
	rateSym     uint64
	elfType     elf.Type
	loadOffset  uint64
	loadVaddr   uint64
	byteOrder   binary.ByteOrder
	symbolTable *gosym.Table
	err         error
}

// target contains the addresses needed to inspect one Go OOM victim.
type target struct {
	startTime   uint64
	version     string
	mbucketsSym uint64
	rateSym     uint64
	loadBias    uint64
	byteOrder   binary.ByteOrder
	symbolTable *gosym.Table
}

func (t *target) mbuckets() uint64 {
	return t.mbucketsSym + t.loadBias
}

func (t *target) rateAddress() uint64 {
	if t.rateSym == 0 {
		return 0
	}
	return t.rateSym + t.loadBias
}

// discoverPID resolves a target from its thread-group leader PID.
func discoverPID(ctx context.Context, procRoot string, pid int) (target, error) {
	if err := ctx.Err(); err != nil {
		return target{}, err
	}
	if pid <= 0 {
		return target{}, errors.New("Go heap target PID must be positive")
	}
	if procRoot == "" {
		procRoot = defaultProcRoot
	}
	return inspectPID(ctx, procRoot, pid)
}

func inspectPID(ctx context.Context, procRoot string, pid int) (target, error) {
	startTimeTicks, err := readStartTimeTicks(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return target{}, err
	}

	exePath := filepath.Join(procRoot, strconv.Itoa(pid), "exe")
	executable, err := os.Open(exePath)
	if err != nil {
		return target{}, err
	}
	defer executable.Close()

	inode, err := executableInode(executable)
	if err != nil {
		return target{}, err
	}
	info := inspectExecutable(ctx, executable)
	if err := ctx.Err(); err != nil {
		return target{}, err
	}
	if info.err != nil {
		return target{}, info.err
	}

	loadBias := uint64(0)
	if info.elfType == elf.ET_DYN {
		mappings, mapsErr := memsnap.ReadProcMapsContext(ctx,
			filepath.Join(procRoot, strconv.Itoa(pid), "maps"), 1<<18)
		if mapsErr != nil {
			return target{}, mapsErr
		}
		loadBias, err = memsnap.FindLoadBias(mappings, inode, info.loadOffset,
			info.loadVaddr)
		if err != nil {
			return target{}, err
		}
	}

	// Reject malformed PIE symbols before relocation can wrap to mapped memory.
	if info.mbucketsSym > ^uint64(0)-loadBias {
		return target{}, errors.New("runtime.mbuckets address overflows PIE load bias")
	}
	if info.rateSym > ^uint64(0)-loadBias {
		return target{}, errors.New("runtime.MemProfileRate address overflows PIE load bias")
	}

	return target{
		startTime:   startTimeTicks,
		version:     info.goVersion,
		mbucketsSym: info.mbucketsSym,
		rateSym:     info.rateSym,
		loadBias:    loadBias,
		byteOrder:   info.byteOrder,
		symbolTable: info.symbolTable,
	}, nil
}

func executableInode(file *os.File) (uint64, error) {
	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("executable stat has no Stat_t")
	}
	return sys.Ino, nil
}

func inspectExecutable(ctx context.Context, file *os.File) imageInfo {
	if err := ctx.Err(); err != nil {
		return imageInfo{err: err}
	}
	elfFile, err := memsnap.ReadELFMetadata(ctx, file)
	if err != nil {
		return imageInfo{err: fmt.Errorf("%w: open ELF: %w",
			errUnsupportedRuntime, err)}
	}
	defer elfFile.Close()
	if elfFile.Class != elf.ELFCLASS64 {
		return imageInfo{err: fmt.Errorf("%w: unsupported Go victim ELF class %s",
			errUnsupportedRuntime, elfFile.Class)}
	}

	var symbolTable *gosym.Table
	loadSymbolTable := func() (*gosym.Table, error) {
		if symbolTable != nil {
			return symbolTable, nil
		}
		table, tableErr := newGoSymbolTable(ctx, elfFile)
		if tableErr == nil {
			symbolTable = table
		}
		return table, tableErr
	}
	runtimeSymbols := lookupRuntimeSymbols(ctx, elfFile)
	if err := ctx.Err(); err != nil {
		return imageInfo{err: err}
	}
	mbucketsSym := runtimeSymbols.mbuckets
	if !runtimeSymbols.mbucketsFound {
		table, tableErr := loadSymbolTable()
		if tableErr != nil {
			err = fmt.Errorf("%w: %w", errMBucketsSymbolNotFound, tableErr)
		} else {
			mbucketsSym, err = lookupStrippedMBuckets(elfFile, table)
		}
	}
	if err != nil {
		return imageInfo{err: err}
	}
	rateSym := runtimeSymbols.memProfileRate
	if !runtimeSymbols.memProfileRateFound {
		// The provider can still return raw samples if scaling metadata is not
		// recoverable from a stripped executable.
		if table, tableErr := loadSymbolTable(); tableErr == nil {
			rateSym, _ = lookupStrippedRate(elfFile, table)
		}
	}
	loadOffset, loadVaddr, err := firstLoadSegment(elfFile)
	if err != nil {
		return imageInfo{err: fmt.Errorf("%w: %w", errUnsupportedRuntime, err)}
	}
	goVersion, err := readBuildVersion(ctx, elfFile)
	if err != nil {
		return imageInfo{err: fmt.Errorf("%w: read Go build info: %w",
			errUnsupportedRuntime, err)}
	}
	if err := ctx.Err(); err != nil {
		return imageInfo{err: err}
	}
	if err := validateGoVersion(goVersion); err != nil {
		return imageInfo{err: err}
	}

	return imageInfo{
		goVersion:   goVersion,
		mbucketsSym: mbucketsSym,
		rateSym:     rateSym,
		elfType:     elfFile.Type,
		loadOffset:  loadOffset,
		loadVaddr:   loadVaddr,
		byteOrder:   elfFile.ByteOrder,
		symbolTable: symbolTable,
	}
}

// Reuse validated ELF headers instead of letting debug/buildinfo parse them again.
// Only the inline version used by Go 1.18+ is needed, not the module graph.
func readBuildVersion(ctx context.Context, file *elf.File) (string, error) {
	if section := file.Section(".go.buildinfo"); section != nil {
		return readBuildVersionData(ctx, section.Open())
	}
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD && program.Flags&(elf.PF_X|elf.PF_W) == elf.PF_W {
			return readBuildVersionData(ctx, program.Open())
		}
	}
	return "", fmt.Errorf("Go build info section or writable data segment is unavailable")
}

func readBuildVersionData(ctx context.Context, source io.Reader) (string, error) {
	reader := bufio.NewReader(io.LimitReader(source, maxELFMetadataBytes))
	var header [32]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// The linker aligns build info to 16 bytes, including in sectionless ELF.
		if _, err := io.ReadFull(reader, header[:16]); err != nil {
			return "", fmt.Errorf("Go build info not found within metadata budget: %w", err)
		}
		if !bytes.HasPrefix(header[:16], []byte("\xff Go buildinf:")) {
			continue
		}
		if _, err := io.ReadFull(reader, header[16:]); err != nil {
			return "", fmt.Errorf("read Go build info header: %w", err)
		}
		if header[15]&2 == 0 {
			return "", fmt.Errorf("Go build info predates supported inline version format")
		}
		size, err := binary.ReadUvarint(reader)
		if err != nil {
			return "", fmt.Errorf("read Go build version length: %w", err)
		}
		if size == 0 || size > 256 {
			return "", fmt.Errorf("Go build version length %d exceeds metadata budget", size)
		}
		version := make([]byte, int(size))
		if _, err := io.ReadFull(reader, version); err != nil {
			return "", fmt.Errorf("read Go build version: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return string(version), nil
	}
}

type runtimeSymbolAddresses struct {
	mbuckets            uint64
	memProfileRate      uint64
	mbucketsFound       bool
	memProfileRateFound bool
}

func lookupRuntimeSymbols(ctx context.Context, file *elf.File) runtimeSymbolAddresses {
	addresses := runtimeSymbolAddresses{}
	collect := func(symbols []elf.Symbol) {
		for _, symbol := range symbols {
			switch symbol.Name {
			case "runtime.mbuckets":
				if !addresses.mbucketsFound {
					addresses.mbuckets = symbol.Value
					addresses.mbucketsFound = true
				}
			case "runtime.MemProfileRate":
				if !addresses.memProfileRateFound {
					addresses.memProfileRate = symbol.Value
					addresses.memProfileRateFound = true
				}
			}
			if addresses.mbucketsFound && addresses.memProfileRateFound {
				return
			}
		}
	}
	wanted := func(name string) bool {
		return name == "runtime.mbuckets" || name == "runtime.MemProfileRate"
	}
	for _, typ := range []elf.SectionType{elf.SHT_SYMTAB, elf.SHT_DYNSYM} {
		if symbols, err := memsnap.ReadELFSymbols(ctx, file, typ,
			maxELFMetadataBytes, maxELFSymbols, wanted); err == nil {
			collect(symbols)
		}
		if addresses.mbucketsFound && addresses.memProfileRateFound {
			break
		}
	}
	return addresses
}

func firstLoadSegment(file *elf.File) (uint64, uint64, error) {
	pageSize := uint64(os.Getpagesize())
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD {
			return alignDown(program.Off, pageSize), alignDown(program.Vaddr, pageSize), nil
		}
	}
	return 0, 0, errors.New("ELF has no PT_LOAD segment")
}

func readStartTimeTicks(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return memsnap.ParseProcStatStartTime(data)
}

func alignDown(value, alignment uint64) uint64 {
	return value &^ (alignment - 1)
}

// validateGoVersion makes an unknown runtime fail closed instead of silently
// interpreting incompatible heap-profile structures.
func validateGoVersion(version string) error {
	runtimeVersion := version
	start := strings.Index(version, "go")
	if start >= 0 {
		version = strings.Fields(version[start:])[0]
	}
	languageVersion := versionpkg.Lang(version)
	if languageVersion == "" ||
		versionpkg.Compare(languageVersion, minGoVersion) < 0 ||
		versionpkg.Compare(languageVersion, maxGoVersion) > 0 {
		return fmt.Errorf("%w: unsupported Go runtime version %q: supported range is %s-%s",
			errUnsupportedRuntime, runtimeVersion, minGoVersion,
			maxGoVersion)
	}
	return nil
}
