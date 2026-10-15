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

package profile

import (
	"context"
	"encoding/json"
)

// ParseCollapsedData parses a collapsed profile (e.g. asprof output) into *Data.
//
// e.g., Java example:
// HotCode.main;HotCode.hotmethod3;java/util/UUID.randomUUID;java/security/SecureRandom.nextBytes;
// read;entry_SYSCALL_64_after_hwframe_[k];do_syscall_64_[k];ksys_read_[k];__check_object_size_[k] 1
func ParseCollapsedData(ctx context.Context, input *ParseInput) (*Data, error) {
	var outputs []SampleOutput
	if err := json.Unmarshal(input.Data, &outputs); err == nil && len(outputs) > 0 {
		for _, out := range outputs {
			if out.PID == 0 {
				goto fallback
			}
		}
		return parseMultiProcessData(ctx, input, outputs, true)
	}

fallback:
	return parseCommonData(ctx, input)
}
