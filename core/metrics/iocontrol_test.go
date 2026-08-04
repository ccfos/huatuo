// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Shared IO counter and kernel-layout contracts are checked with reusable fixtures.

package collector

import (
	"context"
	"testing"
	"time"

	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestIOControlAttachRetries(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  error
		failures int
		attempts int
		want     error
	}{
		{name: "available", attempts: 1},
		{name: "missing", failure: unix.ENOENT, failures: 1, attempts: 1, want: types.ErrNotSupported},
		{name: "unsupported", failure: unix.EOPNOTSUPP, failures: 1, attempts: 1, want: types.ErrNotSupported},
		{name: "permission", failure: unix.EPERM, failures: 1, attempts: 1, want: unix.EPERM},
		{name: "transient", failure: unix.EIO, failures: 2, attempts: 3},
		{name: "exhausted", failure: unix.EIO, failures: 4, attempts: 3, want: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			err := retryIOControlAttach(t.Context(), time.Millisecond, func() error {
				attempts++
				if attempts <= test.failures {
					return test.failure
				}
				return nil
			})
			require.ErrorIs(t, err, test.want)
			require.Equal(t, test.attempts, attempts)
			if test.want != nil {
				require.ErrorIs(t, err, test.failure)
			}
		})
	}
	t.Run("cancel retry wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		attempts := 0
		err := retryIOControlAttach(ctx, time.Hour, func() error {
			attempts++
			cancel()
			return unix.EIO
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, attempts)
	})
}
