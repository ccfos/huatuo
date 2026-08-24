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

package tracing

import (
	"fmt"
	"slices"

	"huatuo-bamai/pkg/observation"
)

// Type identifies an on-demand tracing capability.
type Type string

const (
	TypeUnknown        Type = ""
	TypeNetworkingDrop Type = "networking_drop"
	TypeIO             Type = "io_tracing"
	TypeTCPRetransmit  Type = "tcp_retransmit"
)

// Capability describes an available on-demand tracing capability.
type Capability struct {
	Type            Type
	SupportedScopes []observation.Scope
}

type capabilityDefinition struct {
	Capability
	available bool
}

var capabilityDefinitions = []capabilityDefinition{
	{
		Capability: Capability{
			Type:            TypeNetworkingDrop,
			SupportedScopes: []observation.Scope{observation.ScopeHost},
		},
	},
	{
		Capability: Capability{
			Type:            TypeIO,
			SupportedScopes: []observation.Scope{observation.ScopeHost},
		},
	},
	{
		Capability: Capability{
			Type:            TypeTCPRetransmit,
			SupportedScopes: []observation.Scope{observation.ScopeHost},
		},
	},
}

// ParseType parses a public on-demand tracing type.
func ParseType(value string) (Type, error) {
	typ := Type(value)
	if _, ok := capabilityFor(typ); ok {
		return typ, nil
	}
	return TypeUnknown, fmt.Errorf("unsupported tracing type %q", value)
}

// Capabilities returns tracing capabilities backed by executors in this build.
func Capabilities() []Capability {
	result := make([]Capability, 0, len(capabilityDefinitions))
	for i := range capabilityDefinitions {
		definition := &capabilityDefinitions[i]
		if definition.available {
			result = append(result, cloneCapability(&definition.Capability))
		}
	}
	return result
}

// IsAvailable reports whether this build contains an executor for the type.
func IsAvailable(typ Type) bool {
	definition, ok := capabilityFor(typ)
	return ok && definition.available
}

// SupportsScope reports whether a tracing type defines the requested scope.
func SupportsScope(typ Type, scope observation.Scope) bool {
	definition, ok := capabilityFor(typ)
	return ok && slices.Contains(definition.SupportedScopes, scope)
}

func capabilityFor(typ Type) (*capabilityDefinition, bool) {
	for i := range capabilityDefinitions {
		if capabilityDefinitions[i].Type == typ {
			return &capabilityDefinitions[i], true
		}
	}
	return nil, false
}

func cloneCapability(capability *Capability) Capability {
	result := *capability
	result.SupportedScopes = slices.Clone(capability.SupportedScopes)
	return result
}
