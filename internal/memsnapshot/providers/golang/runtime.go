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
	"context"
	"debug/elf"
	"debug/gosym"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/symbol"
)

const (
	maxELFMetadataBytes = 64 << 20
	maxELFSymbols       = 1 << 20
)

var errUnsupportedRuntime = errors.New("Go runtime is unsupported")

var errMBucketsSymbolNotFound = errors.New("runtime.mbuckets symbol not found")

type runtimeInfo struct {
	version        string
	mbucketsHead   uint64
	memProfileRate int64 // Negative means unavailable; zero means disabled.
	loadBias       uint64
	layout         runtimeLayout
}

func (r *runtimeInfo) readMemProfileRate(pid int, address uint64) int64 {
	memory := processMemory{pid: pid}
	var raw [8]byte
	if err := memory.readInto(address, raw[:]); err != nil {
		return -1
	}
	rate := int64(r.layout.byteOrder.Uint64(raw[:]))
	if rate < 0 {
		return -1
	}
	return rate
}

// newRuntimeInfo keeps fallback symbols local to runtime address recovery.
func newRuntimeInfo(ctx context.Context, pid int, elfFile *elf.File, goVersion string) (*runtimeInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	layout, err := newRuntimeLayout(goVersion, elfFile.Class, elfFile.ByteOrder)
	if err != nil {
		return nil, err
	}

	var symbolTable *gosym.Table
	loadSymbolTable := func() (*gosym.Table, error) {
		if symbolTable != nil {
			return symbolTable, nil
		}
		if elfFile.Machine != elf.EM_X86_64 {
			return nil, fmt.Errorf("stripped %s binaries are unsupported", elfFile.Machine)
		}
		table, tableErr := symbol.ReadGoTable(ctx, elfFile)
		if tableErr == nil {
			symbolTable = table
		}
		return table, tableErr
	}
	runtimeSymbols, symbolErr := lookupRuntimeSymbols(ctx, elfFile)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mbucketsSym := runtimeSymbols.mbuckets
	if !runtimeSymbols.hasMBuckets {
		table, tableErr := loadSymbolTable()
		if tableErr != nil {
			err = fmt.Errorf("%w: %w", errMBucketsSymbolNotFound, tableErr)
		} else {
			mbucketsSym, err = lookupStrippedMBuckets(elfFile, table)
		}
	}
	if err != nil {
		return nil, errors.Join(symbolErr, err)
	}
	rateSym := runtimeSymbols.memProfileRate
	var rateError error
	if !runtimeSymbols.hasMemProfileRate {
		table, tableErr := loadSymbolTable()
		if tableErr != nil {
			rateError = tableErr
		} else {
			rateSym, rateError = lookupStrippedRate(elfFile, table)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info := &runtimeInfo{
		version:        goVersion,
		memProfileRate: -1,
		layout:         layout,
	}
	loadBias, err := resolveExecutableLoadBias(ctx, pid, elfFile)
	if err != nil {
		return nil, err
	}
	info.loadBias = loadBias
	if rateError == nil && rateSym != 0 && rateSym <= ^uint64(0)-loadBias {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info.memProfileRate = info.readMemProfileRate(pid, rateSym+loadBias)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info.memProfileRate != 0 {
		if mbucketsSym > ^uint64(0)-loadBias {
			return nil, errors.New("runtime.mbuckets address overflows PIE load bias")
		}
		memory := processMemory{pid: pid}
		var raw [8]byte
		readErr := memory.readInto(mbucketsSym+loadBias, raw[:])
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, fmt.Errorf("read runtime.mbuckets head: %w", readErr)
		}
		info.mbucketsHead = info.layout.byteOrder.Uint64(raw[:])
	}
	return info, nil
}

type runtimeSymbolAddresses struct {
	mbuckets          uint64
	memProfileRate    uint64
	hasMBuckets       bool
	hasMemProfileRate bool
}

func lookupRuntimeSymbols(ctx context.Context, file *elf.File) (runtimeSymbolAddresses, error) {
	var failures []error
	addresses := runtimeSymbolAddresses{}
	collect := func(symbols []elf.Symbol) {
		for _, symbol := range symbols {
			switch symbol.Name {
			case "runtime.mbuckets":
				if !addresses.hasMBuckets {
					addresses.mbuckets = symbol.Value
					addresses.hasMBuckets = true
				}
			case "runtime.MemProfileRate":
				if !addresses.hasMemProfileRate {
					addresses.memProfileRate = symbol.Value
					addresses.hasMemProfileRate = true
				}
			}
			if addresses.hasMBuckets && addresses.hasMemProfileRate {
				return
			}
		}
	}
	wanted := func(name string) bool {
		return name == "runtime.mbuckets" || name == "runtime.MemProfileRate"
	}
	for _, typ := range []elf.SectionType{elf.SHT_SYMTAB, elf.SHT_DYNSYM} {
		if symbols, err := symbol.ReadELFSymbols(ctx, file, typ,
			maxELFMetadataBytes, maxELFSymbols, wanted); err == nil {
			collect(symbols)
		} else if !errors.Is(err, elf.ErrNoSymbols) {
			if ctx.Err() != nil {
				return addresses, ctx.Err()
			}
			failures = append(failures, fmt.Errorf("read %s runtime symbols: %w", typ, err))
		}
		if addresses.hasMBuckets && addresses.hasMemProfileRate {
			break
		}
	}
	return addresses, errors.Join(failures...)
}
