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

// ParseRawData parses a raw profile (e.g. py-spy output) into *Data.
//
// e.g., Python example:
// process 3577332:"python /app/test.py"; __bootstrap (threading.py:784);__bootstrap_inner (threading.py:811);
// run (threading.py:764);worker (more-complex-demo.py:22); level1 (more-complex-demo.py:18);level2 (more-complex-demo.py:15) 10
func ParseRawData(ctx context.Context, input *ParseInput) (*Data, error) {
	var outputs []SampleOutput
	if err := json.Unmarshal(input.Data, &outputs); err == nil && len(outputs) > 0 {
		for _, out := range outputs {
			if out.PID == 0 {
				goto fallback
			}
		}
		return parseMultiProcessData(ctx, input, outputs, false)
	}

fallback:
	return parseCommonData(ctx, input)
}
