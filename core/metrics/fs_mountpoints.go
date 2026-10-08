// Copyright 2025, 2026 The HuaTuo Authors
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
	"path"
	"sort"

	prometheusprocfs "github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
)

type mountPointCollector struct{}

func init() {
	tracing.RegisterEventTracing("mountpoint_perm", newMountPoint)
}

func newMountPoint() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &mountPointCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *mountPointCollector) Update() ([]*metric.Data, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	mountinfo, err := fs.GetMounts()
	if err != nil {
		return nil, err
	}

	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.MountPointStat.MountPointsIncluded, "")
	if err != nil {
		return nil, fmt.Errorf("mount point filter: %w", err)
	}

	metrics := []*metric.Data{}
	for _, v := range visibleMountPoints(mountinfo) {
		if !f.Match(v.MountPoint) {
			continue
		}

		mountTag := map[string]string{"mountpoint": v.MountPoint}
		ro := 0
		if _, ok := v.Options["ro"]; ok {
			ro = 1
		}

		metrics = append(metrics,
			metric.NewGaugeData("ro", float64(ro), "whether mountpoint is readonly or not", mountTag))
	}
	return metrics, nil
}

// visibleMountPoints follows the mount tree rather than mount IDs or file order.
// Overmounting a mount hides its descendants, including entries at other paths.
func visibleMountPoints(mounts []*prometheusprocfs.MountInfo) []*prometheusprocfs.MountInfo {
	byID := make(map[int]*prometheusprocfs.MountInfo, len(mounts))
	children := make(map[int][]*prometheusprocfs.MountInfo)
	for _, mount := range mounts {
		byID[mount.MountID] = mount
	}
	var roots []*prometheusprocfs.MountInfo
	for _, mount := range mounts {
		if byID[mount.ParentID] == nil || mount.ParentID == mount.MountID {
			roots = append(roots, mount)
		} else {
			children[mount.ParentID] = append(children[mount.ParentID], mount)
		}
	}
	pending := uncoveredMountSiblings(roots)
	visited := make(map[int]bool, len(mounts))
	visible := make([]*prometheusprocfs.MountInfo, 0, len(mounts))
	for len(pending) > 0 {
		mount := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for mount != nil {
			if visited[mount.MountID] {
				mount = nil
				break
			}
			visited[mount.MountID] = true
			var overmount *prometheusprocfs.MountInfo
			for _, child := range children[mount.MountID] {
				if child.MountPoint == mount.MountPoint {
					overmount = child
					break
				}
			}
			if overmount == nil {
				break
			}
			mount = overmount
		}
		if mount == nil {
			continue
		}
		visible = append(visible, mount)
		pending = append(pending, uncoveredMountSiblings(children[mount.MountID])...)
	}
	return visible
}

// A sibling mounted at a path prefix hides a mount attached earlier below that
// path. A visible later submount would name the prefix mount as its parent.
func uncoveredMountSiblings(mounts []*prometheusprocfs.MountInfo) []*prometheusprocfs.MountInfo {
	sort.Slice(mounts, func(i, j int) bool {
		if len(mounts[i].MountPoint) != len(mounts[j].MountPoint) {
			return len(mounts[i].MountPoint) < len(mounts[j].MountPoint)
		}
		return mounts[i].MountPoint < mounts[j].MountPoint
	})
	selected := make(map[string]bool, len(mounts))
	result := make([]*prometheusprocfs.MountInfo, 0, len(mounts))
	for _, mount := range mounts {
		hidden := selected[mount.MountPoint]
		for parent := path.Dir(mount.MountPoint); !hidden; parent = path.Dir(parent) {
			hidden = selected[parent]
			if parent == "/" || parent == "." {
				break
			}
		}
		if hidden {
			continue
		}
		selected[mount.MountPoint] = true
		result = append(result, mount)
	}
	return result
}
