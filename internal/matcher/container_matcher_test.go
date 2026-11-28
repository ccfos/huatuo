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

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/observation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContainer is shared across container-related tests.
var testContainer = &pod.Container{
	Hostname: "test-host",
	Labels:   map[string]any{observation.FieldContainerNamespace: "application-ns"},
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
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerHostname, "[invalid")},
		nil,
	)
	require.Error(t, err)
}

func TestNewContainerMatcher_InvalidExcludePattern(t *testing.T) {
	_, err := NewContainerMatcher(
		nil,
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerQoS, "[invalid")},
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
		{"namespace matched", observation.FieldContainerNamespace, "^application-", true},
		{"namespace unmatched", observation.FieldContainerNamespace, "^kube-system", false},
		{"hostname matched", observation.FieldContainerHostname, "test-.*", true},
		{"hostname unmatched", observation.FieldContainerHostname, "prod-.*", false},
		{"qos matched", observation.FieldContainerQoS, "guaranteed", true},
		{"qos unmatched", observation.FieldContainerQoS, "besteffort", false},
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
		{"namespace matched → excluded", observation.FieldContainerNamespace, "^application-", false},
		{"namespace unmatched → kept", observation.FieldContainerNamespace, "^kube-system", true},
		{"qos matched → excluded", observation.FieldContainerQoS, "guaranteed", false},
		{"qos unmatched → kept", observation.FieldContainerQoS, "besteffort", true},
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
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerHostname, "prod-")},
	)
	require.NoError(t, err)
	assert.True(t, cm.Match(testContainer))

	// in include AND in exclude → exclude wins
	cmBoth, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{containerSpec(observation.FieldContainerHostname, "test-")},
	)
	require.NoError(t, err)
	assert.False(t, cmBoth.Match(testContainer))
}

// --- Match: AND-logic within include list ---

func TestContainerMatcher_MultipleIncludeSpecsAND(t *testing.T) {
	// all specs match → passes
	cmAll, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{
			containerSpec(observation.FieldContainerNamespace, "^application-"),
			containerSpec(observation.FieldContainerHostname, "test-"),
		}, nil,
	)
	require.NoError(t, err)
	assert.True(t, cmAll.Match(testContainer))

	// one spec fails → filtered out
	cmPartial, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{
			containerSpec(observation.FieldContainerNamespace, "^application-"),
			containerSpec(observation.FieldContainerHostname, "prod-"),
		}, nil,
	)
	require.NoError(t, err)
	assert.False(t, cmPartial.Match(testContainer))
}

// --- Match: OR-logic within exclude list ---

func TestContainerMatcher_MultipleExcludeSpecsOR(t *testing.T) {
	// a single exclude rule that hits is enough to skip the container
	cmPartial, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{
			containerSpec(FieldTypeContainerHostname, "prod-"),
			containerSpec(FieldTypeContainerQos, "guaranteed"),
		},
	)
	require.NoError(t, err)
	assert.False(t, cmPartial.Match(testContainer))

	// no exclude rule hits, so the container passes
	cmNone, err := NewContainerMatcher(
		[]FieldSpec[*pod.Container]{containerSpec(FieldTypeContainerHostNamespace, "^application-")},
		[]FieldSpec[*pod.Container]{
			containerSpec(FieldTypeContainerHostname, "prod-"),
			containerSpec(FieldTypeContainerQos, "besteffort"),
		},
	)
	require.NoError(t, err)
	assert.True(t, cmNone.Match(testContainer))
}

// --- NewContainerMatcherFromRules ---

func TestNewContainerMatcherFromRules_Include(t *testing.T) {
	cm, err := NewContainerMatcherFromRules(
		[]*Rule{{Field: observation.FieldContainerNamespace, Pattern: "^application-"}},
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
		[]*Rule{{Field: observation.FieldContainerHostname, Pattern: "[invalid"}},
		nil,
	)
	require.Error(t, err)
}
