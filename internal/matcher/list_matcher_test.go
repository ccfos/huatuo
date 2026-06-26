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

func TestListMatcherMatch(t *testing.T) {
	m, err := NewListMatcher([]string{"eth0", "bond[0-9]+"})
	require.NoError(t, err)

	require.True(t, m.Match("eth0"))
	require.True(t, m.Match("bond4"))
	require.False(t, m.Match("eth0.100"))
	require.False(t, m.Match("bond"))
}

func TestListMatcherFilter(t *testing.T) {
	m, err := NewListMatcher([]string{"eth.*", "lo"})
	require.NoError(t, err)

	got := m.Filter([]string{"lo", "docker0", "eth0", "eth1"})
	require.Equal(t, []string{"lo", "eth0", "eth1"}, got)
}

func TestListMatcherEmptyMatchesNothing(t *testing.T) {
	m, err := NewListMatcher(nil)
	require.NoError(t, err)

	require.False(t, m.Match("eth0"))
	require.Empty(t, m.Filter([]string{"eth0"}))
}

func TestListMatcherInvalidPattern(t *testing.T) {
	_, err := NewListMatcher([]string{"eth["})
	require.Error(t, err)
	require.Contains(t, err.Error(), "eth[")
}
