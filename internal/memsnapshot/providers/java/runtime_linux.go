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

package java

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/symbol"

	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	maxProcMapEntries   = 1 << 18
	maxELFMetadataBytes = 32 << 20
	maxELFSymbols       = 1 << 20
)

type addressRange struct {
	start uint64
	end   uint64
}

type vmImage struct {
	javaVersion string
	vmRelease   string
	symbols     map[string]uint64
	readable    []addressRange
}

func (image *vmImage) displayVersion() string {
	if image == nil {
		return ""
	}
	if image.javaVersion != "" {
		return image.javaVersion
	}
	return image.vmRelease
}

func (image *vmImage) contains(address, size uint64) bool {
	if image == nil || len(image.readable) == 0 {
		return true
	}
	end, ok := checkedAdd(address, size)
	if !ok {
		return false
	}
	index := sort.Search(len(image.readable), func(index int) bool {
		return image.readable[index].end > address
	})
	return index < len(image.readable) && image.readable[index].start <= address &&
		end <= image.readable[index].end
}

func discoverVM(ctx context.Context, procRoot string, pid int) (*vmImage, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	mapsPath := filepath.Join(procRoot, strconv.Itoa(pid), "maps")
	mappings, err := memsnapshot.ReadProcMapsContext(ctx, mapsPath, maxProcMapEntries)
	if err != nil {
		return nil, fmt.Errorf("read HotSpot maps: %w", err)
	}
	readable := make([]addressRange, 0, len(mappings))
	for _, mapping := range mappings {
		if !strings.HasPrefix(mapping.Perms, "r") {
			continue
		}
		if len(readable) >= maxProcMapEntries {
			return nil, unsupportedHotSpot(
				"readable mapping count exceeds safety limit",
			)
		}
		readable = append(readable, addressRange{
			start: mapping.Start, end: mapping.End,
		})
	}
	var mappedPath string
	var selectedMap memsnapshot.ProcMap
	for _, mapping := range mappings {
		path := procfs.TrimDeletedSuffix(mapping.Path)
		if !strings.HasSuffix(path, "/libjvm.so") {
			continue
		}
		mappedPath = path
		selectedMap = mapping
		break
	}
	if mappedPath == "" {
		return nil, fmt.Errorf("%w: target does not map libjvm.so",
			errHotSpotUnavailable)
	}
	imagePath := filepath.Join(procRoot, strconv.Itoa(pid), "root", mappedPath)
	imageFile, err := memsnapshot.OpenMappedFile(imagePath, selectedMap)
	if err != nil {
		return nil, fmt.Errorf("open target libjvm.so: %w", err)
	}
	defer imageFile.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := elf.NewFile(imageFile)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, fmt.Errorf("read target libjvm.so ELF metadata: %w", err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.ByteOrder != binary.LittleEndian {
		return nil, fmt.Errorf("%w: unsupported ELF class or byte order",
			errHotSpotUnavailable)
	}
	loadBias, err := memsnapshot.FindELFLoadBias(file, mappings, &selectedMap)
	if err != nil {
		return nil, fmt.Errorf("determine libjvm.so load bias: %w", err)
	}
	dynamicSymbols, err := symbol.ReadELFSymbols(ctx, file, elf.SHT_DYNSYM,
		maxELFMetadataBytes, maxELFSymbols, func(name string) bool {
			return strings.HasPrefix(name, "gHotSpotVM")
		})
	if err != nil {
		if errors.Is(err, elf.ErrNoSymbols) {
			return nil, unsupportedHotSpot("libjvm.so has no dynamic symbols")
		}
		return nil, fmt.Errorf("read libjvm.so dynamic symbols: %w", err)
	}
	symbols := make(map[string]uint64, len(dynamicSymbols))
	for _, symbol := range dynamicSymbols {
		name := strings.SplitN(symbol.Name, "@", 2)[0]
		if strings.HasPrefix(name, "gHotSpotVM") {
			address, valid := checkedAdd(loadBias, symbol.Value)
			if valid {
				symbols[name] = address
			}
		}
	}
	version, err := readJavaVersion(ctx, procRoot, pid, mappedPath)
	if err != nil {
		return nil, fmt.Errorf("read Java release: %w", err)
	}
	return &vmImage{
		javaVersion: version, symbols: symbols, readable: readable,
	}, nil
}
