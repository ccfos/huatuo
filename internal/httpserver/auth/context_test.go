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

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrincipalContext(t *testing.T) {
	t.Parallel()

	_, ok := PrincipalFromContext(context.Background())
	require.False(t, ok)

	principal := Principal{
		ID:          "user-2026",
		Permissions: []Permission{"GET /v1/profiling/**"},
	}
	ctx := WithPrincipal(context.Background(), principal)
	principal.Permissions[0] = "changed"

	got, ok := PrincipalFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, Permission("GET /v1/profiling/**"), got.Permissions[0])

	got.Permissions[0] = "changed again"
	fresh, ok := PrincipalFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, Permission("GET /v1/profiling/**"), fresh.Permissions[0])
}
