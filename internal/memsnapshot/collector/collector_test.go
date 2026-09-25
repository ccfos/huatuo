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

package collector

import (
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func processIdentityForTest(t *testing.T) memsnapshot.ProcessIdentity {
	t.Helper()
	identity, err := memsnapshot.ReadIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestSnapshotRejectsInvalidOptions(t *testing.T) {
	identity := processIdentityForTest(t)
	for _, options := range []Options{
		{TopK: -1},
		{TopK: memsnapshot.MaxMemoryObjectEntries + 1},
		{SnapshotTimeout: -time.Second},
	} {
		result, err := Snapshot(t.Context(), identity, options)
		if err == nil || result != nil {
			t.Fatalf("invalid options %+v accepted: result=%+v err=%v", options, result, err)
		}
	}
}
