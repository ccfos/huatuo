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

package profiling

import (
	"fmt"
	"slices"
)

type Type string

const (
	TypeUnknown Type = ""
	TypeCPU     Type = "cpu"
	TypeMemory  Type = "memory"
	TypeLock    Type = "lock"
)

type Language string

const (
	LanguageUnknown Language = ""
	LanguageC       Language = "c"
	LanguageCPP     Language = "c++"
	LanguageGo      Language = "go"
	LanguageJava    Language = "java"
	LanguagePython  Language = "python"
)

type MemoryMode string

const (
	MemoryModeUnknown       MemoryMode = ""
	MemoryModeObjectAlloc   MemoryMode = "object_alloc"
	MemoryModeObjectUsage   MemoryMode = "object_usage"
	MemoryModeVirtualAlloc  MemoryMode = "virtual_alloc"
	MemoryModePhysicalAlloc MemoryMode = "physical_alloc"
	MemoryModePhysicalUsage MemoryMode = "physical_usage"
)

type Implementation string

const (
	ImplementationUnknown Implementation = ""
	ImplementationNative  Implementation = "native"
	ImplementationJava    Implementation = "java"
	ImplementationPython  Implementation = "python"
)

type capability struct {
	Language       Language
	Implementation Implementation
	Types          []Type
	MemoryModes    []MemoryMode
}

var capabilities = []capability{
	newNativeCapability(LanguageC),
	newNativeCapability(LanguageCPP),
	newNativeCapability(LanguageGo),
	{
		Language:       LanguageJava,
		Implementation: ImplementationJava,
		Types:          []Type{TypeCPU, TypeMemory},
		MemoryModes:    []MemoryMode{MemoryModeObjectAlloc, MemoryModeObjectUsage},
	},
	{
		Language:       LanguagePython,
		Implementation: ImplementationPython,
		Types:          []Type{TypeCPU},
		MemoryModes:    []MemoryMode{},
	},
}

func newNativeCapability(language Language) capability {
	return capability{
		Language:       language,
		Implementation: ImplementationNative,
		Types:          []Type{TypeCPU, TypeMemory, TypeLock},
		MemoryModes: []MemoryMode{
			MemoryModeVirtualAlloc,
			MemoryModePhysicalAlloc,
			MemoryModePhysicalUsage,
		},
	}
}

func ParseType(value string) (Type, error) {
	typ := Type(value)
	if typ == TypeCPU || typ == TypeMemory || typ == TypeLock {
		return typ, nil
	}
	return TypeUnknown, fmt.Errorf("unsupported profiling type %q (expected: cpu, memory, or lock)", value)
}

func ParseLanguage(value string) (Language, error) {
	language := Language(value)
	for _, capability := range capabilities {
		if capability.Language == language {
			return language, nil
		}
	}
	return LanguageUnknown, fmt.Errorf("unsupported language %q", value)
}

func ParseMemoryMode(value string) (MemoryMode, error) {
	mode := MemoryMode(value)
	for _, capability := range capabilities {
		if slices.Contains(capability.MemoryModes, mode) {
			return mode, nil
		}
	}
	return MemoryModeUnknown, fmt.Errorf("unsupported memory mode %q", value)
}

func IsSupported(language Language, typ Type) bool {
	capability, ok := capabilityFor(language)
	return ok && slices.Contains(capability.Types, typ)
}

func SupportsMemoryMode(language Language, mode MemoryMode) bool {
	capability, ok := capabilityFor(language)
	return ok && slices.Contains(capability.MemoryModes, mode)
}

func LanguagesFor(typ Type) []Language {
	languages := make([]Language, 0, len(capabilities))
	for _, capability := range capabilities {
		if slices.Contains(capability.Types, typ) {
			languages = append(languages, capability.Language)
		}
	}
	return languages
}

func MemoryModesFor(language Language) []MemoryMode {
	capability, ok := capabilityFor(language)
	if !ok {
		return []MemoryMode{}
	}
	return slices.Clone(capability.MemoryModes)
}

func ImplementationFor(language Language) (Implementation, bool) {
	capability, ok := capabilityFor(language)
	if !ok {
		return ImplementationUnknown, false
	}
	return capability.Implementation, true
}

func capabilityFor(language Language) (capability, bool) {
	for _, capability := range capabilities {
		if capability.Language == language {
			return capability, true
		}
	}
	return capability{}, false
}
