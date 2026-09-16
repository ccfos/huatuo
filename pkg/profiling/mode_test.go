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

package profiling

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOffCPUPhase(t *testing.T) {
	for _, phase := range []OffCPUPhase{OffCPUPhaseAll, OffCPUPhaseBlocked, OffCPUPhaseRunqueue} {
		got, err := ParseOffCPUPhase(string(phase))
		require.NoError(t, err)
		require.Equal(t, phase, got)
	}
	phase, err := ParseOffCPUPhase("wait")
	require.Equal(t, OffCPUPhaseUnknown, phase)
	require.EqualError(t, err, `unsupported off-CPU phase "wait"`)
}

func allMemoryModes() []Mode {
	return []Mode{
		ModeObjectAlloc,
		ModeObjectUsage,
		ModeVirtualAlloc,
		ModePhysicalAlloc,
		ModePhysicalUsage,
	}
}
