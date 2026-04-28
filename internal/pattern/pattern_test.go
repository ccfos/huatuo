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

package pattern

import (
	"testing"

	"huatuo-bamai/internal/pod"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testContainer = &pod.Container{
	Hostname: "test-host",
	Labels:   map[string]any{"HostNamespace": "application-ns"},
	Qos:      pod.ContainerQos(1),
}

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		issues  [][]string
		value   string
		want    string
		matched bool
	}{
		{"empty list", nil, "any", "none", false},
		{"empty value", [][]string{{"x", "[a-z]+"}}, "", "none", false},
		{"exact match", [][]string{{"softlockup", "softlockup"}}, "softlockup", "softlockup", true},
		{"regex match", [][]string{{"mem", "memory.*"}}, "memory_pressure", "mem", true},
		{
			"multi match second",
			[][]string{{"a", "aaa"}, {"b", "bbb"}, {"c", "ccc"}},
			"bbb", "b", true,
		},
		{"no match", [][]string{{"a", "aaa"}, {"b", "bbb"}}, "xxx", "none", false},
		{"wrong group len 1", [][]string{{"x"}}, "any", "none", false},
		{"wrong group len 3", [][]string{{"x", "y", "z"}}, "any", "none", false},
		{"anchors match", [][]string{{"x", "^foo$"}}, "foo", "x", true},
		{"anchors no match", [][]string{{"x", "^foo$"}}, "foobar", "none", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := Match(tt.issues, tt.value)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.matched, matched)
		})
	}
}

func TestRule(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		tests := []struct {
			name     string
			pattern  string
			value    string
			expected bool
		}{
			{"exact", "hello", "hello", true},
			{"partial", "hello", "hello world", true},
			{"no match", "world", "hello", false},
			{"empty value", ".*", "", false},
			{"anchors", "^ns$", "ns", true},
			{"anchors partial", "^ns$", "ns-partial", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.expected, (&Rule{Pattern: tt.pattern}).match(tt.value))
			})
		}
	})

	t.Run("matchContainer", func(t *testing.T) {
		tests := []struct {
			name     string
			field    string
			pattern  string
			expected bool
		}{
			{"namespace matched", FieldTypeContainerHostNamespace, "^application-", true},
			{"namespace unmatched", FieldTypeContainerHostNamespace, "^kube-system", false},
			{"hostname matched", FieldTypeContainerHostname, "test-.*", true},
			{"hostname unmatched", FieldTypeContainerHostname, "prod-.*", false},
			{"qos matched", FieldTypeContainerQos, "guaranteed", true},
			{"qos unmatched", FieldTypeContainerQos, "besteffort", false},
			{"empty field", "", ".*", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := &Rule{Field: tt.field, Pattern: tt.pattern}
				assert.Equal(t, tt.expected, r.matchContainer(testContainer))
			})
		}
	})
}

func TestNewFilter(t *testing.T) {
	tests := []struct {
		name      string
		included  string
		excluded  string
		wantInLen int
		wantExLen int
	}{
		{"empty", "", "", 0, 0},
		{"include only", "inc", "", 1, 0},
		{"exclude only", "", "exc", 0, 1},
		{"both", "inc", "exc", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFilter(tt.included, tt.excluded)
			require.NotNil(t, f)
			assert.Len(t, f.Included, tt.wantInLen)
			assert.Len(t, f.Excluded, tt.wantExLen)
		})
	}
}

func TestFilter(t *testing.T) {
	f := &Filter{
		Included: []*Rule{{Pattern: "^allowed"}},
		Excluded: []*Rule{{Pattern: "bad"}},
	}

	t.Run("Ignored", func(t *testing.T) {
		assert.False(t, (&Filter{}).Ignored("any"))
		assert.True(t, (&Filter{Excluded: []*Rule{{Pattern: "test"}}}).Ignored("test"))
		assert.False(t, (&Filter{Included: []*Rule{{Pattern: "test"}}}).Ignored("test"))
		assert.True(t, (&Filter{Included: []*Rule{{Pattern: "test"}}}).Ignored("other"))
		assert.True(t, f.Ignored("bad-value"))   // excluded matches
		assert.False(t, f.Ignored("allowed-ok")) // included, not excluded
		assert.True(t, f.Ignored("other"))       // not included
	})

	t.Run("IgnoreContainer", func(t *testing.T) {
		assert.False(t, (&Filter{}).IgnoreContainer(testContainer))

		excludeQos := &Filter{Excluded: []*Rule{{Field: FieldTypeContainerQos, Pattern: "guaranteed"}}}
		assert.True(t, excludeQos.IgnoreContainer(testContainer))

		includeNs := &Filter{Included: []*Rule{{Field: FieldTypeContainerHostNamespace, Pattern: "^application-"}}}
		assert.False(t, includeNs.IgnoreContainer(testContainer))

		includeOther := &Filter{Included: []*Rule{{Field: FieldTypeContainerHostNamespace, Pattern: "^kube-system"}}}
		assert.True(t, includeOther.IgnoreContainer(testContainer))
	})
}
