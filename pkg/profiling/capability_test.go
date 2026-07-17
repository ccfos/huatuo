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
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilities(t *testing.T) {
	nativeModes := []MemoryMode{
		MemoryModeVirtualAlloc,
		MemoryModePhysicalAlloc,
		MemoryModePhysicalUsage,
	}
	tests := []struct {
		language       Language
		implementation Implementation
		types          []Type
		memoryModes    []MemoryMode
	}{
		{LanguageC, ImplementationNative, []Type{TypeCPU, TypeMemory, TypeLock}, nativeModes},
		{LanguageCPP, ImplementationNative, []Type{TypeCPU, TypeMemory, TypeLock}, nativeModes},
		{LanguageGo, ImplementationNative, []Type{TypeCPU, TypeMemory, TypeLock}, nativeModes},
		{
			LanguageJava,
			ImplementationJava,
			[]Type{TypeCPU, TypeMemory},
			[]MemoryMode{MemoryModeObjectAlloc, MemoryModeObjectUsage},
		},
		{LanguagePython, ImplementationPython, []Type{TypeCPU}, []MemoryMode{}},
	}

	for _, tt := range tests {
		t.Run(string(tt.language), func(t *testing.T) {
			implementation, ok := ImplementationFor(tt.language)
			require.True(t, ok)
			require.Equal(t, tt.implementation, implementation)

			for _, typ := range []Type{TypeCPU, TypeMemory, TypeLock} {
				require.Equal(t, slices.Contains(tt.types, typ), IsSupported(tt.language, typ))
			}
			require.Equal(t, tt.memoryModes, MemoryModesFor(tt.language))
			for _, mode := range allMemoryModes() {
				require.Equal(
					t,
					slices.Contains(tt.memoryModes, mode),
					SupportsMemoryMode(tt.language, mode),
				)
			}
		})
	}

	require.Equal(
		t,
		[]Language{LanguageC, LanguageCPP, LanguageGo, LanguageJava, LanguagePython},
		LanguagesFor(TypeCPU),
	)
	require.Equal(
		t,
		[]Language{LanguageC, LanguageCPP, LanguageGo, LanguageJava},
		LanguagesFor(TypeMemory),
	)
	require.Equal(
		t,
		[]Language{LanguageC, LanguageCPP, LanguageGo},
		LanguagesFor(TypeLock),
	)
}

func TestMemoryModesForReturnsCopy(t *testing.T) {
	modes := MemoryModesFor(LanguageJava)
	modes[0] = MemoryModePhysicalUsage

	require.Equal(t, MemoryModeObjectAlloc, MemoryModesFor(LanguageJava)[0])
}

func TestCapabilityDefinitionsAreUnique(t *testing.T) {
	languages := map[Language]bool{}
	for _, capability := range capabilities {
		require.NotEqual(t, LanguageUnknown, capability.Language)
		require.NotEqual(t, ImplementationUnknown, capability.Implementation)
		require.False(t, languages[capability.Language], "duplicate language %q", capability.Language)
		languages[capability.Language] = true
		require.Equal(t, len(capability.Types), len(unique(capability.Types)))
		require.Equal(t, len(capability.MemoryModes), len(unique(capability.MemoryModes)))
	}
}

func TestParsers(t *testing.T) {
	for _, typ := range []Type{TypeCPU, TypeMemory, TypeLock} {
		parsed, err := ParseType(string(typ))
		require.NoError(t, err)
		require.Equal(t, typ, parsed)
	}
	for _, language := range []Language{
		LanguageC,
		LanguageCPP,
		LanguageGo,
		LanguageJava,
		LanguagePython,
	} {
		parsed, err := ParseLanguage(string(language))
		require.NoError(t, err)
		require.Equal(t, language, parsed)
	}
	for _, mode := range allMemoryModes() {
		parsed, err := ParseMemoryMode(string(mode))
		require.NoError(t, err)
		require.Equal(t, mode, parsed)
	}

	_, err := ParseLanguage("rust")
	require.EqualError(t, err, `unsupported language "rust"`)
	_, err = ParseMemoryMode("unknown")
	require.EqualError(t, err, `unsupported memory mode "unknown"`)
}

func TestParseTypeRejectsLegacyMemoryValue(t *testing.T) {
	_, err := ParseType("mem")
	require.EqualError(t, err, `unsupported profiling type "mem" (expected: cpu, memory, or lock)`)
}

func allMemoryModes() []MemoryMode {
	return []MemoryMode{
		MemoryModeObjectAlloc,
		MemoryModeObjectUsage,
		MemoryModeVirtualAlloc,
		MemoryModePhysicalAlloc,
		MemoryModePhysicalUsage,
	}
}

func unique[T comparable](values []T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
