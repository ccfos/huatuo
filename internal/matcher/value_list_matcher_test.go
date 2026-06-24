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

func TestValueListMatcher_EmptyListMatchesNothing(t *testing.T) {
	m, err := NewValueListMatcher(nil)
	require.NoError(t, err)

	require.False(t, m.Match("eth0"))
}

func TestValueListMatcher_ExactDeviceName(t *testing.T) {
	m, err := NewValueListMatcher([]string{"eth0"})
	require.NoError(t, err)

	require.True(t, m.Match("eth0"))
	require.False(t, m.Match("veth0"))
}

func TestValueListMatcher_RegexDeviceName(t *testing.T) {
	m, err := NewValueListMatcher([]string{"eth[0-9]+", "bond.*"})
	require.NoError(t, err)

	require.True(t, m.Match("eth0"))
	require.True(t, m.Match("eth12"))
	require.True(t, m.Match("bond4"))
	require.False(t, m.Match("ens3"))
}

func TestValueListMatcher_RegexMustMatchWholeValue(t *testing.T) {
	m, err := NewValueListMatcher([]string{"eth"})
	require.NoError(t, err)

	require.True(t, m.Match("eth"))
	require.False(t, m.Match("eth0"))
}

func TestValueListMatcher_InvalidPattern(t *testing.T) {
	_, err := NewValueListMatcher([]string{"eth0", "[bad"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "index 1")
	require.Contains(t, err.Error(), "[bad")
}
