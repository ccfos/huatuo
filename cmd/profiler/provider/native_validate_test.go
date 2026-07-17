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

func TestResolveProbability(t *testing.T) {
	probability, err := resolveProbability(100)
	require.NoError(t, err)
	require.Equal(t, uint(100), probability)

	_, err = resolveProbability(0)
	require.EqualError(t, err, "physical memory probability must be between 1 and 100")

	_, err = resolveProbability(101)
	require.EqualError(t, err, "physical memory probability must be between 1 and 100")
}
