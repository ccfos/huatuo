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

package provider

import (
	"fmt"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/types"
)

func readProcessLabels(pid int) ([]types.ProfileLabel, error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("invalid profiling pid %d", pid)
	}

	labels := []types.ProfileLabel{types.ProfilePIDLabel(uint32(pid))}
	proc, err := procfs.NewProc(pid)
	if err != nil {
		return nil, err
	}

	stat, err := proc.Stat()
	if err != nil {
		return nil, err
	}

	labels = append(labels, types.ProfileStringLabel("comm", stat.Comm))

	return labels, nil
}
