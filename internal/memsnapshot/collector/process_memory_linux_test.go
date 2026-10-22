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
	"encoding/json"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestProcessMemory(t *testing.T) {
	full := "VmSize: 100 kB\nVmRSS: 60 kB\nRssAnon: 40 kB\nRssFile: 20 kB\nRssShmem: 0 kB\nVmSwap: 0 kB\nVmPTE: 4 kB\n"
	m := parseProcessMemory(strings.NewReader(full))
	if m.Status != memsnapshot.SnapshotStatusComplete || *m.RSSBytes != 60*1024 || *m.PageTableBytes != 4096 {
		t.Fatalf("memory = %+v", m)
	}
	for _, input := range []string{"VmRSS: 0 kB\n", "VmRSS: 0 kB\nVmSwap: bad kB\nVmSize: 18446744073709551615 kB\n"} {
		m = parseProcessMemory(strings.NewReader(input))
		data, err := json.Marshal(m)
		if err != nil || m.Status != memsnapshot.SnapshotStatusPartial || m.RSSBytes == nil || *m.RSSBytes != 0 ||
			m.SwapBytes != nil || m.VirtualBytes != nil || !strings.Contains(string(data), `"rss_bytes":0`) || strings.Contains(string(data), `"swap_bytes"`) {
			t.Fatalf("missing fields confused with zero: %s, %v", data, err)
		}
	}
	if m := readProcessMemory(-1); m.Status != memsnapshot.SnapshotStatusUnavailable || m.RSSBytes != nil {
		t.Fatalf("missing process = %+v", m)
	}
}
