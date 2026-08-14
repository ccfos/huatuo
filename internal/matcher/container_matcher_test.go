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

package matcher

import (
	"testing"

	"huatuo-bamai/internal/pod"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContainer is shared across container-related tests.
var testContainer = &pod.Container{
	Hostname: "test-host",
	Labels:   map[string]any{"HostNamespace": "application-ns"},
	Qos:      pod.ContainerQos(1),
}

func containerSpec(field, pattern string) FieldSpec[*pod.Container] {
	return FieldSpec[*pod.Container]{
		Name:    field,
		Pattern: pattern,
		Extract: containerFieldExtractor(field),
	}
}

// --- NewContainerMatcher ---

func TestNewContainerMatcher_Nil(t *testing.T) {
	var cm *ContainerMatcher
	assert.True(t, cm.Match(testContainer))
}

func TestNewContainerMatcher_NoSpecs(t *testing.T) {
	cm, err := NewContainerMatcher(nil, nil)
	require.NoError(t, err)
	assert.True(t, cm.Match(testContainer))
}

func TestNewContainerMatcher_InvalidIncludePattern(t *testing.T) {
	_, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostname, "[invalid")},
		nil,
	)
	require.Error(t, err)
}

func TestNewContainerMatcher_InvalidExcludePattern(t *testing.T) {
	_, err := NewContainerMatcher(
		nil,
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerQos, "[invalid")},
	)
	require.Error(t, err)
}

// --- Match: include semantics ---

func TestContainerMatcher_Include(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		pattern   string
		wantMatch bool
	}{
		{"namespace matched", FieldTypeContainerHostNamespace, "^application-", true},
		{"namespace unmatched", FieldTypeContainerHostNamespace, "^kube-system", false},
		{"hostname matched", FieldTypeContainerHostname, "test-.*", true},
		{"hostname unmatched", FieldTypeContainerHostname, "prod-.*", false},
		{"qos matched", FieldTypeContainerQos, "guaranteed", true},
		{"qos unmatched", FieldTypeContainerQos, "besteffort", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, err := NewContainerMatcher([]FieldSpec[*pod.Container]{containerSpec(tt.field, tt.pattern)}, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMatch, cm.Match(testContainer))
		})
	}
}

// --- Match: exclude semantics ---

func TestContainerMatcher_Exclude(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		pattern   string
		wantMatch bool
	}{
		{"namespace matched → excluded", FieldTypeContainerHostNamespace, "^application-", false},
		{"namespace unmatched → kept", FieldTypeContainerHostNamespace, "^kube-system", true},
		{"qos matched → excluded", FieldTypeContainerQos, "guaranteed", false},
		{"qos unmatched → kept", FieldTypeContainerQos, "besteffort", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, err := NewContainerMatcher(nil, []FieldSpec[*pod.Container]{containerSpec(tt.field, tt.pattern)})
			require.NoError(t, err)
			assert.Equal(t, tt.wantMatch, cm.Match(testContainer))
		})
	}
}

// --- Match: include + exclude combined ---

func TestContainerMatcher_IncludeAndExclude(t *testing.T) {
	// in include, not in exclude → passes
	cm, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostname, "prod-")},
	)
	require.NoError(t, err)
	assert.True(t, cm.Match(testContainer))

	// in include AND in exclude → exclude wins
	cmBoth, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostname, "test-")},
	)
	require.NoError(t, err)
	assert.False(t, cmBoth.Match(testContainer))
}

// --- Match: AND-logic within include list ---

func TestContainerMatcher_MultipleIncludeSpecsAND(t *testing.T) {
	// all specs match → passes
	cmAll, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{
			containerSpec(FieldTypeContainerHostNamespace, "^application-"),
			containerSpec(FieldTypeContainerHostname, "test-"),
		}, nil,
	)
	require.NoError(t, err)
	assert.True(t, cmAll.Match(testContainer))

	// one spec fails → filtered out
	cmPartial, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{
			containerSpec(FieldTypeContainerHostNamespace, "^application-"),
			containerSpec(FieldTypeContainerHostname, "prod-"),
		}, nil,
	)
	require.NoError(t, err)
	assert.False(t, cmPartial.Match(testContainer))
}

// --- NewContainerMatcherFromRules ---

func TestNewContainerMatcherFromRules_Include(t *testing.T) {
	cm, err := NewContainerMatcherFromRules(
		[]*Rule{{Field: FieldTypeContainerHostNamespace, Pattern: "^application-"}},
		nil,
	)
	require.NoError(t, err)
	assert.True(t, cm.Match(testContainer))
}

func TestNewContainerMatcherFromRules_SkipsEmptyField(t *testing.T) {
	// Rule with no Field is silently skipped; empty matcher always matches
	cm, err := NewContainerMatcherFromRules(
		[]*Rule{{Field: "", Pattern: ".*"}},
		nil,
	)
	require.NoError(t, err)
	assert.True(t, cm.Match(testContainer))
}

func TestNewContainerMatcherFromRules_InvalidPattern(t *testing.T) {
	_, err := NewContainerMatcherFromRules(
		[]*Rule{{Field: FieldTypeContainerHostname, Pattern: "[invalid"}},
		nil,
	)
	require.Error(t, err)
}

func TestNewContainerMatcherFromRules_RejectsUnknownField(t *testing.T) {
	tests := []struct {
		name    string
		include []*Rule
		exclude []*Rule
		want    string
	}{
		{
			name:    "include",
			include: []*Rule{{Field: "container_hostnam", Pattern: ".*"}},
			want:    "include container rules",
		},
		{
			name:    "exclude",
			exclude: []*Rule{{Field: "container_hostnam", Pattern: ".*"}},
			want:    "exclude container rules",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewContainerMatcherFromRules(test.include, test.exclude)
			require.ErrorContains(t, err, test.want)
			require.ErrorContains(t, err, "container_hostnam")
		})
	}
}
