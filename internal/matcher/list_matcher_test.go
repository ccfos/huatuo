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

func TestListMatcher_Match_EmptyListMatchesNothing(t *testing.T) {
	lm, err := NewListMatcher(nil)
	require.NoError(t, err)

	require.False(t, lm.Match("eth0"))
}

func TestListMatcher_Match_ExactName(t *testing.T) {
	lm, err := NewListMatcher([]string{"eth0", "eth1", "eth0.100"})
	require.NoError(t, err)

	require.True(t, lm.Match("eth0"))
	require.True(t, lm.Match("eth0.100"))
	require.False(t, lm.Match("veth0"))
	require.False(t, lm.Match("eth0x100"))
}

func TestListMatcher_Match_RegexName(t *testing.T) {
	lm, err := NewListMatcher([]string{"eth[0-9]+"})
	require.NoError(t, err)

	require.True(t, lm.Match("eth0"))
	require.True(t, lm.Match("eth10"))
	require.False(t, lm.Match("veth0"))
}

func TestNewListMatcher_InvalidPattern(t *testing.T) {
	_, err := NewListMatcher([]string{"[bad"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "[bad")
}
