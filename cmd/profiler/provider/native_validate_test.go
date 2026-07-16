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

package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateNativePIDs(t *testing.T) {
	require.NoError(t, validateNativePIDs("CPU", []int{1}))
	require.EqualError(
		t,
		validateNativePIDs("CPU", []int{1, 2}),
		"start native CPU profiler: multiple PIDs are not supported",
	)
}

func TestValidateNativeMemoryExtraFlags(t *testing.T) {
	require.NoError(t, validateNativeMemoryExtraFlags(modePhysicalAlloc, map[string]string{"probability": "10"}))
	require.EqualError(
		t,
		validateNativeMemoryExtraFlags(modePhysicalAlloc, map[string]string{"unknown": "1"}),
		`native memory profiler does not support --flags key "unknown"`,
	)
	require.EqualError(
		t,
		validateNativeMemoryExtraFlags(modeVirtualAlloc, map[string]string{"probability": "10"}),
		`--flags probability is not supported by memory mode "virtual_alloc"`,
	)
}

func TestResolveProbability(t *testing.T) {
	probability, err := resolveProbability("", modePhysicalAlloc)
	require.NoError(t, err)
	require.Equal(t, uint(100), probability)

	_, err = resolveProbability("0", modePhysicalAlloc)
	require.EqualError(t, err, "probability must be between 1 and 100")

	_, err = resolveProbability("101", modePhysicalUsage)
	require.EqualError(t, err, "probability must be between 1 and 100")
}
