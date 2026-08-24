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

	"huatuo-bamai/pkg/observation"
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
		cpuModes       []CPUMode
		memoryModes    []MemoryMode
	}{
		{
			LanguageC,
			ImplementationNative,
			[]Type{TypeCPU, TypeMemory},
			[]CPUMode{CPUModeOnCPU, CPUModeOffCPU},
			nativeModes,
		},
		{
			LanguageCPP,
			ImplementationNative,
			[]Type{TypeCPU, TypeMemory},
			[]CPUMode{CPUModeOnCPU, CPUModeOffCPU},
			nativeModes,
		},
		{
			LanguageGo,
			ImplementationNative,
			[]Type{TypeCPU, TypeMemory},
			[]CPUMode{CPUModeOnCPU, CPUModeOffCPU},
			nativeModes,
		},
		{
			LanguageJava,
			ImplementationJava,
			[]Type{TypeCPU, TypeMemory},
			[]CPUMode{CPUModeOnCPU},
			[]MemoryMode{MemoryModeObjectAlloc, MemoryModeObjectUsage},
		},
		{
			LanguagePython,
			ImplementationPython,
			[]Type{TypeCPU},
			[]CPUMode{CPUModeOnCPU},
			[]MemoryMode{},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.language), func(t *testing.T) {
			implementation, ok := ImplementationFor(tt.language)
			require.True(t, ok)
			require.Equal(t, tt.implementation, implementation)

			for _, typ := range []Type{TypeCPU, TypeMemory, TypeLock} {
				require.Equal(t, slices.Contains(tt.types, typ), IsSupported(tt.language, typ))
			}
			require.Equal(t, tt.cpuModes, CPUModesFor(tt.language))
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
	require.Empty(t, LanguagesFor(TypeLock))
}

func TestMemoryModesForReturnsCopy(t *testing.T) {
	modes := MemoryModesFor(LanguageJava)
	modes[0] = MemoryModePhysicalUsage

	require.Equal(t, MemoryModeObjectAlloc, MemoryModesFor(LanguageJava)[0])
}

func TestCPUModesForReturnsCopy(t *testing.T) {
	modes := CPUModesFor(LanguageGo)
	modes[0] = CPUModeUnknown

	require.Equal(t, CPUModeOnCPU, CPUModesFor(LanguageGo)[0])
}

func TestCapabilityDefinitionsAreUnique(t *testing.T) {
	keys := map[struct {
		typ      Type
		language Language
	}]bool{}
	for _, capability := range capabilities {
		key := struct {
			typ      Type
			language Language
		}{typ: capability.Type, language: capability.Language}

		require.NotEqual(t, TypeUnknown, capability.Type)
		require.NotEqual(t, LanguageUnknown, capability.Language)
		require.NotEqual(t, ImplementationUnknown, capability.implementation)
		require.False(t, keys[key], "duplicate capability %q/%q", capability.Type, capability.Language)
		keys[key] = true
		require.NotEmpty(t, capability.Modes)
		require.NotEmpty(t, capability.SupportedScopes)
		require.Equal(t, len(capability.Modes), len(unique(capability.Modes)))
		require.Equal(t, len(capability.SupportedScopes), len(unique(capability.SupportedScopes)))
	}
}

func TestCapabilitiesReturnsDeepCopy(t *testing.T) {
	got := Capabilities()
	require.NotEmpty(t, got)

	got[0].Type = TypeMemory
	got[0].Modes[0] = Mode("changed")
	got[0].SupportedScopes[0] = "changed"

	fresh := Capabilities()
	require.Equal(t, TypeCPU, fresh[0].Type)
	require.Equal(t, ModeOnCPU, fresh[0].Modes[0])
	require.Equal(t, "host", string(fresh[0].SupportedScopes[0]))
}

func TestCapabilitiesExposeSupportedScopeAndBinaryMatch(t *testing.T) {
	containerOnly := []observation.Scope{observation.ScopeContainer}
	hostAndContainer := []observation.Scope{observation.ScopeHost, observation.ScopeContainer}

	for _, capability := range Capabilities() {
		switch {
		case capability.Type == TypeCPU && capability.implementation == ImplementationNative:
			require.Equal(t, hostAndContainer, capability.SupportedScopes)
			require.False(t, capability.SupportsBinaryMatch)
		case capability.Type == TypeCPU:
			require.Equal(t, containerOnly, capability.SupportedScopes)
			require.True(t, capability.SupportsBinaryMatch)
		case capability.Type == TypeMemory:
			require.Equal(t, containerOnly, capability.SupportedScopes)
			require.False(t, capability.SupportsBinaryMatch)
		default:
			t.Fatalf("unexpected capability type %q", capability.Type)
		}
	}
}

func TestParsers(t *testing.T) {
	for _, typ := range []Type{TypeCPU, TypeMemory} {
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
	_, err := ParseLanguage("rust")
	require.EqualError(t, err, `unsupported language "rust"`)

	for _, mode := range []Mode{
		ModeOnCPU,
		ModeOffCPU,
		ModeObjectAlloc,
		ModeObjectUsage,
		ModeVirtualAlloc,
		ModePhysicalAlloc,
		ModePhysicalUsage,
	} {
		parsed, err := ParseMode(string(mode))
		require.NoError(t, err)
		require.Equal(t, mode, parsed)
	}
	_, err = ParseMode("wall_clock")
	require.EqualError(t, err, `unsupported profiling mode "wall_clock"`)
}

func TestParseTypeRejectsLegacyMemoryValue(t *testing.T) {
	_, err := ParseType("mem")
	require.EqualError(t, err, `unsupported profiling type "mem"`)
}

func unique[T comparable](values []T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
