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

	"huatuo-bamai/pkg/observation"
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

type Implementation string

const (
	ImplementationUnknown Implementation = ""
	ImplementationNative  Implementation = "native"
	ImplementationJava    Implementation = "java"
	ImplementationPython  Implementation = "python"
)

// Capability describes one supported profiling type and language combination.
type Capability struct {
	Type                Type
	Language            Language
	Modes               []Mode
	SupportsBinaryMatch bool
	SupportedScopes     []observation.Scope

	implementation Implementation
}

var capabilities = []Capability{
	newNativeCapability(LanguageC, TypeCPU),
	newNativeCapability(LanguageCPP, TypeCPU),
	newNativeCapability(LanguageGo, TypeCPU),
	{
		Type:                TypeCPU,
		Language:            LanguageJava,
		Modes:               []Mode{ModeOnCPU},
		SupportsBinaryMatch: true,
		SupportedScopes:     []observation.Scope{observation.ScopeContainer},
		implementation:      ImplementationJava,
	},
	{
		Type:                TypeCPU,
		Language:            LanguagePython,
		Modes:               []Mode{ModeOnCPU},
		SupportsBinaryMatch: true,
		SupportedScopes:     []observation.Scope{observation.ScopeContainer},
		implementation:      ImplementationPython,
	},
	newNativeCapability(LanguageC, TypeMemory),
	newNativeCapability(LanguageCPP, TypeMemory),
	newNativeCapability(LanguageGo, TypeMemory),
	{
		Type:            TypeMemory,
		Language:        LanguageJava,
		Modes:           []Mode{ModeObjectAlloc, ModeObjectUsage},
		SupportedScopes: []observation.Scope{observation.ScopeContainer},
		implementation:  ImplementationJava,
	},
}

func newNativeCapability(language Language, typ Type) Capability {
	modes := []Mode{ModeOnCPU, ModeOffCPU}
	scopes := []observation.Scope{observation.ScopeHost, observation.ScopeContainer}
	if typ == TypeMemory {
		modes = []Mode{
			ModeVirtualAlloc,
			ModePhysicalAlloc,
			ModePhysicalUsage,
		}
		scopes = []observation.Scope{observation.ScopeContainer}
	}

	return Capability{
		Type:            typ,
		Language:        language,
		Modes:           modes,
		SupportedScopes: scopes,
		implementation:  ImplementationNative,
	}
}

// Capabilities returns the static product capabilities in stable order.
func Capabilities() []Capability {
	result := make([]Capability, len(capabilities))
	for i := range capabilities {
		result[i] = cloneCapability(&capabilities[i])
	}
	return result
}

func ParseType(value string) (Type, error) {
	typ := Type(value)
	if typ == TypeCPU || typ == TypeMemory {
		return typ, nil
	}
	return TypeUnknown, fmt.Errorf("unsupported profiling type %q", value)
}

func ParseLanguage(value string) (Language, error) {
	language := Language(value)
	for i := range capabilities {
		capability := &capabilities[i]
		if capability.Language == language {
			return language, nil
		}
	}
	return LanguageUnknown, fmt.Errorf("unsupported language %q", value)
}

func IsSupported(language Language, typ Type) bool {
	_, ok := capabilityFor(language, typ)
	return ok
}

// SupportsMode reports whether a profiling capability supports a mode.
func SupportsMode(language Language, typ Type, mode Mode) bool {
	capability, ok := capabilityFor(language, typ)
	return ok && slices.Contains(capability.Modes, mode)
}

// SupportsScope reports whether a profiling combination can observe a scope.
func SupportsScope(language Language, typ Type, scope observation.Scope) bool {
	capability, ok := capabilityFor(language, typ)
	return ok && slices.Contains(capability.SupportedScopes, scope)
}

// ModesFor returns the modes supported by one profiling capability.
func ModesFor(language Language, typ Type) []Mode {
	capability, ok := capabilityFor(language, typ)
	if !ok {
		return nil
	}
	return slices.Clone(capability.Modes)
}

// ImplementationFor returns the implementation for one supported capability.
func ImplementationFor(language Language, typ Type) (Implementation, bool) {
	capability, ok := capabilityFor(language, typ)
	if !ok {
		return ImplementationUnknown, false
	}
	return capability.implementation, true
}

func capabilityFor(language Language, typ Type) (*Capability, bool) {
	for i := range capabilities {
		capability := &capabilities[i]
		if capability.Language == language && capability.Type == typ {
			return capability, true
		}
	}
	return nil, false
}

func cloneCapability(capability *Capability) Capability {
	result := *capability
	result.Modes = slices.Clone(capability.Modes)
	result.SupportedScopes = slices.Clone(capability.SupportedScopes)
	return result
}
