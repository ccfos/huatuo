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
	"testing"

	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
)

func TestMountPointMetrics(t *testing.T) {
	mount := &procfs.MountInfo{
		MountPoint: "/data",
		Source:     "/dev/vdb1",
		FSType:     "ext4",
		Options:    map[string]string{"ro": ""},
	}
	stat := &unix.Statfs_t{
		Bsize:  4096,
		Blocks: 100,
		Bfree:  40,
		Bavail: 30,
		Files:  20,
		Ffree:  10,
	}

	got := mountPointMetrics(mount, stat)
	want := []float64{409600, 163840, 122880, 20, 10, 1}
	if len(got) != len(want) {
		t.Fatalf("metric count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Value != want[i] {
			t.Errorf("metric %d value = %v, want %v", i, got[i].Value, want[i])
		}
		labels := got[i].Labels()
		if labels["mountpoint"] != "/data" || labels["device"] != "/dev/vdb1" ||
			labels["fstype"] != "ext4" {
			t.Errorf("metric %d labels = %v", i, labels)
		}
	}
}
