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

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
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
		{"invalid regex", [][]string{{"x", "["}}, "any", "none", false},
		{"anchors match", [][]string{{"x", "^foo$"}}, "foo", "x", true},
		{"anchors no match", [][]string{{"x", "^foo$"}}, "foobar", "none", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := Classify(tt.issues, tt.value)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.matched, matched)
		})
	}
}

func TestValidateClassifications(t *testing.T) {
	tests := []struct {
		name    string
		rules   [][]string
		wantErr string
	}{
		{name: "empty list"},
		{name: "valid", rules: [][]string{{"memory", "memory.*"}}},
		{name: "missing expression", rules: [][]string{{"memory"}}, wantErr: "rule 0"},
		{name: "empty name", rules: [][]string{{"", "memory.*"}}, wantErr: "empty name"},
		{
			name:    "invalid expression",
			rules:   [][]string{{"memory", "["}},
			wantErr: `rule 0 "memory" has invalid regular expression "["`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClassifications(tt.rules)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
