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

package java

import (
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestJavaBoundedCaptureStatus(t *testing.T) {
	sampled := uint64(8192)
	snapshot := &memsnapshot.Snapshot{}
	finishStatus(snapshot, 0, 1<<20, sampled)
	if snapshot.Status != memsnapshot.StatusPartial || !strings.Contains(snapshot.Reason, "bounded") {
		t.Fatalf("bounded sample not marked partial: %+v", snapshot)
	}
}
