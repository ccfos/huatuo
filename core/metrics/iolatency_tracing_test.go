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

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
)

func TestTrackContainersWithBlkioCSS(t *testing.T) {
	containers := map[string]*pod.Container{
		"with-blkio": {
			CgroupCss: map[string]uint64{subsystem.SubsystemBlkIO: 10},
		},
		"without-blkio": {},
	}

	tracked := trackContainersWithBlkioCSS(containers)
	if _, ok := tracked["with-blkio"]; !ok {
		t.Fatal("container with blkio CSS was not tracked")
	}
	if _, ok := tracked["without-blkio"]; ok {
		t.Fatal("container without blkio CSS was tracked")
	}
}
