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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/pkg/observation"
)

func TestCapabilityDefinitions(t *testing.T) {
	t.Parallel()
	wantTypes := []Type{TypeNetworkingDrop, TypeIO, TypeTCPRetransmit}
	require.Len(t, capabilityDefinitions, len(wantTypes))
	seen := make(map[Type]struct{}, len(capabilityDefinitions))
	for i, definition := range capabilityDefinitions {
		require.Equal(t, wantTypes[i], definition.Type)
		require.NotEqual(t, TypeUnknown, definition.Type)
		require.Equal(t, []observation.Scope{observation.ScopeHost}, definition.SupportedScopes)
		require.False(t, definition.available)
		_, duplicate := seen[definition.Type]
		require.False(t, duplicate, "duplicate tracing type %q", definition.Type)
		seen[definition.Type] = struct{}{}
	}
}

func TestParseType(t *testing.T) {
	t.Parallel()
	for _, typ := range []Type{TypeNetworkingDrop, TypeIO, TypeTCPRetransmit} {
		got, err := ParseType(string(typ))
		require.NoError(t, err)
		require.Equal(t, typ, got)
	}
	for _, value := range []string{"", "dropwatch", "iotracing", "tcpshark"} {
		got, err := ParseType(value)
		require.Equal(t, TypeUnknown, got)
		require.EqualError(t, err, `unsupported tracing type "`+value+`"`)
	}
}

func TestCapabilitiesRemainStaticWithoutExecutors(t *testing.T) {
	t.Parallel()
	capabilities := Capabilities()
	require.Len(t, capabilities, 3)
	capabilities[0].SupportedScopes[0] = observation.ScopeContainer
	require.Equal(t, observation.ScopeHost, Capabilities()[0].SupportedScopes[0])
	for _, typ := range []Type{TypeNetworkingDrop, TypeIO, TypeTCPRetransmit} {
		require.False(t, IsAvailable(typ))
	}
}

func TestCloneCapabilityReturnsDeepCopy(t *testing.T) {
	t.Parallel()
	got := cloneCapability(&capabilityDefinitions[0].Capability)
	got.Type = TypeIO
	got.SupportedScopes[0] = observation.ScopeContainer
	require.Equal(t, TypeNetworkingDrop, capabilityDefinitions[0].Type)
	require.Equal(
		t,
		[]observation.Scope{observation.ScopeHost},
		capabilityDefinitions[0].SupportedScopes,
	)
}
