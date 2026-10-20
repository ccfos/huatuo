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

import "github.com/ccfos/huatuo/pkg/types"

type userStackCacheKey struct {
	PID     uint32
	StackID int32
}

func nativeLabels(pid uint32, comm string) []types.ProfileLabel {
	return []types.ProfileLabel{types.ProfileStringLabel("comm", comm), types.ProfilePIDLabel(pid)}
}
