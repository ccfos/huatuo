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

package procfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNetArpCache(t *testing.T) {
	// Create temporary procfs
	tempDir := t.TempDir()
	var err error

	arpDir := filepath.Join(tempDir, "proc/net/stat")
	err = os.MkdirAll(arpDir, 0o755)
	require.NoError(t, err)

	arpCachePath := filepath.Join(arpDir, "arp_cache")

	RootPrefix(tempDir)
	defer RootPrefix("/")

	t.Run("SuccessfulParse", func(t *testing.T) {
		content := `entries allocs destroys hash_grows lookups hits dests
		0a 0b 0c 0d 0e 0f 10`

		err = os.WriteFile(arpCachePath, []byte(content), 0o600)
		require.NoError(t, err)

		stats, err := NetArpCache()
		require.NoError(t, err)
		require.NotNil(t, stats)
		require.Len(t, stats.Stats, 7)
		require.Equal(t, uint64(10), stats.Stats["entries"])
		require.Equal(t, uint64(11), stats.Stats["allocs"])
		require.Equal(t, uint64(12), stats.Stats["destroys"])
		require.Equal(t, uint64(13), stats.Stats["hash_grows"])
		require.Equal(t, uint64(14), stats.Stats["lookups"])
		require.Equal(t, uint64(15), stats.Stats["hits"])
		require.Equal(t, uint64(16), stats.Stats["dests"])
	})

	t.Run("FileNotFound", func(t *testing.T) {
		_ = os.Remove(arpCachePath)

		stats, err := NetArpCache()
		require.Error(t, err)
		require.Nil(t, stats)
	})

	t.Run("InvalidHex", func(t *testing.T) {
		content := `entries
		invalid`

		err = os.WriteFile(arpCachePath, []byte(content), 0o600)
		require.NoError(t, err)

		stats, err := NetArpCache()
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid syntax")
		require.Nil(t, stats)
	})

	t.Run("MissingSecondLine", func(t *testing.T) {
		content := `entries allocs`

		err = os.WriteFile(arpCachePath, []byte(content), 0o600)
		require.NoError(t, err)

		stats, err := NetArpCache()
		require.NoError(t, err)
		require.NotNil(t, stats)
		require.Empty(t, stats.Stats)
	})

	t.Run("EmptyFile", func(t *testing.T) {
		content := ``

		err = os.WriteFile(arpCachePath, []byte(content), 0o600)
		require.NoError(t, err)

		stats, err := NetArpCache()
		require.NoError(t, err)
		require.NotNil(t, stats)
		require.Empty(t, stats.Stats)
	})

	t.Run("MismatchedFieldsFewerValues", func(t *testing.T) {
		content := `entries allocs destroys
0a 0b`
		err = os.WriteFile(arpCachePath, []byte(content), 0o600)
		require.NoError(t, err)

		stats, err := NetArpCache()
		require.NoError(t, err)
		require.Len(t, stats.Stats, 2)
		require.Equal(t, uint64(10), stats.Stats["entries"])
		require.Equal(t, uint64(11), stats.Stats["allocs"])
	})
}
