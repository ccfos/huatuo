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

	"github.com/stretchr/testify/require"
)

// --- NewValueMatcher ---

func TestNewValueMatcher_EmptyPatterns_NoFilter(t *testing.T) {
	vm, err := NewValueMatcher("", "")
	require.NoError(t, err)
	require.NotNil(t, vm)
	require.Nil(t, vm.include)
	require.Nil(t, vm.exclude)
}

func TestNewValueMatcher_InvalidIncludePattern(t *testing.T) {
	_, err := NewValueMatcher("[bad", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"value"`)
}

func TestNewValueMatcher_InvalidExcludePattern(t *testing.T) {
	_, err := NewValueMatcher("", "[bad")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"value"`)
}

// --- Match: nil receiver ---

func TestValueMatcher_Match_NilReceiver_AlwaysTrue(t *testing.T) {
	var vm *ValueMatcher
	require.True(t, vm.Match("anything"))
	require.True(t, vm.Match(""))
}

// --- Match: no filter ---

func TestValueMatcher_Match_NoFilter_AlwaysTrue(t *testing.T) {
	vm, _ := NewValueMatcher("", "")
	require.True(t, vm.Match("cpu"))
	require.True(t, vm.Match(""))
}

// --- Match: include only ---

func TestValueMatcher_Match_IncludeOnly_Passes(t *testing.T) {
	vm, _ := NewValueMatcher("^cpu$", "")
	require.True(t, vm.Match("cpu"))
}

func TestValueMatcher_Match_IncludeOnly_Fails(t *testing.T) {
	vm, _ := NewValueMatcher("^cpu$", "")
	require.False(t, vm.Match("mem"))
}

// --- Match: exclude only ---

func TestValueMatcher_Match_ExcludeOnly_Excluded(t *testing.T) {
	vm, _ := NewValueMatcher("", "^cpu$")
	require.False(t, vm.Match("cpu"))
}

func TestValueMatcher_Match_ExcludeOnly_NotExcluded(t *testing.T) {
	vm, _ := NewValueMatcher("", "^cpu$")
	require.True(t, vm.Match("mem"))
}

// --- Match: include + exclude ---

func TestValueMatcher_Match_InInclude_NotInExclude(t *testing.T) {
	vm, _ := NewValueMatcher("cpu|mem", "^debug$")
	require.True(t, vm.Match("cpu"))
	require.True(t, vm.Match("mem"))
}

func TestValueMatcher_Match_InInclude_AlsoInExclude(t *testing.T) {
	// exclude wins over include
	vm, _ := NewValueMatcher("cpu|mem", "^cpu$")
	require.False(t, vm.Match("cpu"))
	require.True(t, vm.Match("mem"))
}

func TestValueMatcher_Match_NotInInclude(t *testing.T) {
	vm, _ := NewValueMatcher("^cpu$", "^debug$")
	require.False(t, vm.Match("mem"))
}

// --- Match: regex is an unanchored substring match ---

func TestValueMatcher_Match_UnanchoredSubstring(t *testing.T) {
	// "cpu" matches any value containing the substring "cpu".
	vm, _ := NewValueMatcher("cpu", "")
	require.True(t, vm.Match("cpu"))
	require.True(t, vm.Match("cpu-heavy"))
	require.True(t, vm.Match("high-cpu"))
	require.False(t, vm.Match("memory"))
}
