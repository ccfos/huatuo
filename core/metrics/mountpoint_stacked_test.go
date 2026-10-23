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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	prometheusprocfs "github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestMountPointMetricsSelectVisibleMounts(t *testing.T) {
	cases := []struct {
		name, filter, info string
		want               map[string]float64
	}{
		{
			"stacked different devices", "^/stacked$",
			"398 363 0:46 / /stacked rw - tmpfs none rw\n399 398 0:47 / /stacked ro - tmpfs none ro\n",
			map[string]float64{"/stacked": 1},
		},
		{
			"hidden child under covered parent", "^/foo/bar$",
			"363 362 0:1 / / rw - tmpfs none rw\n398 363 0:46 / /foo rw - tmpfs none rw\n399 398 0:47 / /foo/bar rw - tmpfs none rw\n200 398 0:48 / /foo ro - tmpfs none ro\n201 200 0:49 / /foo/bar ro - tmpfs none ro\n",
			map[string]float64{"/foo/bar": 1},
		},
		{
			"covered sibling and component boundary", "^/(foo|foobar)",
			"363 362 0:1 / / rw - tmpfs none rw\n398 363 0:46 / /foo/bar rw - tmpfs none rw\n399 363 0:47 / /foo ro - tmpfs none ro\n400 399 0:48 / /foo/bar ro - tmpfs none ro\n401 363 0:49 / /foobar rw - tmpfs none rw\n",
			map[string]float64{"/foo": 1, "/foo/bar": 1, "/foobar": 0},
		},
		{
			"ordinary nested mounts", "^/foo",
			"10 9 0:1 / / rw - tmpfs none rw\n11 10 0:2 / /foo rw - tmpfs none rw\n12 11 0:3 / /foo/bar ro - tmpfs none ro\n",
			map[string]float64{"/foo": 0, "/foo/bar": 1},
		},
	}
	for _, tt := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tt.name, reverse), func(t *testing.T) {
				previousConfig := configSnapshot()
				t.Cleanup(func() { Set(previousConfig) })
				cfg := &Config{}
				cfg.MountPointStat.MountPointsIncluded = tt.filter
				Set(cfg)
				previousRoot := filepath.Dir(procfs.DefaultPath())
				t.Cleanup(func() { procfs.RootPrefix(previousRoot) })
				root := t.TempDir()
				file := filepath.Join(root, "proc", "self", "mountinfo")
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				rows := strings.Split(strings.TrimSpace(tt.info), "\n")
				if reverse {
					slices.Reverse(rows)
				}
				if err := os.WriteFile(file, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				procfs.RootPrefix(root)
				metrics, err := (&mountPointCollector{}).Update()
				if err != nil {
					t.Fatal(err)
				}
				got := make(map[string]float64, len(metrics))
				for _, m := range metrics {
					path := m.Labels()["mountpoint"]
					if _, exists := got[path]; exists {
						t.Fatalf("duplicate series for %q", path)
					}
					got[path] = m.Value
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("visible permissions=%v, want %v", got, tt.want)
				}
			})
		}
	}
}

func BenchmarkVisibleMountPoints(b *testing.B) {
	for _, count := range []int{100, 1000} {
		mounts := []*prometheusprocfs.MountInfo{{MountID: 1, ParentID: 0, MountPoint: "/"}}
		for i := 0; i < count; i++ {
			mounts = append(mounts, &prometheusprocfs.MountInfo{MountID: i + 2, ParentID: 1, MountPoint: fmt.Sprintf("/mnt/%d", i)})
		}
		b.Run(fmt.Sprintf("mounts=%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if got := visibleMountPoints(mounts); len(got) != len(mounts) {
					b.Fatal(len(got))
				}
			}
		})
	}
}
