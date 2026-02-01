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
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	ksymbolCounter = 0
	ksymbolPath    = "/proc/kallsyms"
	ksymbolCache   = []Symbol{}
	ksymbolIsInit  = false
	ksymbolMax     = 300000
	ksymbolLock    = sync.Mutex{}
	moduleKernel   = "[kernel]"
	defaultSymbol  = Symbol{0, "", "[unknown]"}
)

const (
	KsymbolStackMaxDepth = 127
	KsymbolStackMinDepth = 16
)

// Stack is record backtrace
type Stack struct {
	BackTrace []string `json:"back_trace"`
}

// Symbol is record kernel symbol info
type Symbol struct {
	Addr   uint64
	Name   string
	Module string
}

func (s Symbol) String() string {
	return fmt.Sprintf("{Addr: %x Name: %s Module: %s}", s.Addr, s.Name, s.Module)
}

type symbolSort []Symbol

func (s symbolSort) Len() int {
	return ksymbolCounter
}

func (s symbolSort) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s symbolSort) Less(i, j int) bool {
	return s[i].Addr < s[j].Addr
}

func loadKAllSymbols() error {
	ksymbolCache = make([]Symbol, ksymbolMax)
	// default
	ksymbolCache[0] = defaultSymbol
	ksymbolCounter = 1

	f, err := os.Open(ksymbolPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		words := strings.FieldsFunc(line, func(c rune) bool {
			return c == ' ' || c == '\t'
		})

		// get kernel and module text symbol
		if len(words) != 3 && len(words) != 4 {
			continue
		}

		// only get text symbol
		if words[1] != "T" && words[1] != "t" {
			continue
		}

		addr, err := strconv.ParseUint(words[0], 16, 64)
		if err != nil {
			fmt.Printf("%v\n", err)
			continue
		}

		if len(words) == 3 {
			ksymbolCache[ksymbolCounter] = Symbol{Addr: addr, Name: words[2], Module: moduleKernel}
		} else {
			ksymbolCache[ksymbolCounter] = Symbol{Addr: addr, Name: words[2], Module: words[3]}
		}
		ksymbolCounter++
	}

	// sort
	sort.Sort(symbolSort(ksymbolCache))

	ksymbolIsInit = true

	return nil
}

func ksymbolSearch(key uint64) Symbol {
	ksymbolLock.Lock()
	defer ksymbolLock.Unlock()

	if !ksymbolIsInit {
		_ = loadKAllSymbols()
	}

	i, j := 0, ksymbolCounter
	for i < j {
		h := (i + j) >> 1 // avoid overflow when computing h
		if ksymbolCache[h].Addr == key {
			i = h
			break
		} else if ksymbolCache[h].Addr < key {
			i = h + 1
		} else {
			j = h
		}
	}

	if ksymbolCache[i].Addr == key {
		return ksymbolCache[i]
	}
	if i == 0 {
		return ksymbolCache[0]
	}
	return ksymbolCache[i-1]
}

// DumpKernelBackTrace converts the kernel stack address to the kernel symbol
// and returns the Stack structure
func DumpKernelBackTrace(stack []uint64, maxDepth int) Stack {
	var s Stack

	for i, addr := range stack {
		if addr == 0 || i >= maxDepth {
			break
		}
		sym := ksymbolSearch(addr)
		if sym.Name != "" {
			offset := addr - sym.Addr
			s.BackTrace = append(s.BackTrace, fmt.Sprintf("%s/+%d %s",
				sym.Name, offset, sym.Module))
		}
	}
	return s
}
