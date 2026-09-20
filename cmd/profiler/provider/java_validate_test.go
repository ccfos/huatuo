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

	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestValidateJavaFrequency(t *testing.T) {
	require.NoError(t, validateJavaFrequency(1))
	require.NoError(t, validateJavaFrequency(1000))
	require.Error(t, validateJavaFrequency(0))
	require.Error(t, validateJavaFrequency(1001))
}

func TestValidateJavaMemoryMode(t *testing.T) {
	args, err := validateJavaMemoryMode(profiling.ModeObjectAlloc)
	require.NoError(t, err)
	require.Empty(t, args)

	args, err = validateJavaMemoryMode(profiling.ModeObjectUsage)
	require.NoError(t, err)
	require.Equal(t, []string{"--live"}, args)

	_, err = validateJavaMemoryMode("unknown")
	require.EqualError(t, err, `unsupported Java memory mode "unknown"`)
}
