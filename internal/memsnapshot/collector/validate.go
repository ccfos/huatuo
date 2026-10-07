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
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func (o *Options) setDefaults() {
	if o.TopK == 0 {
		o.TopK = 10
	}
	if o.SnapshotTimeout == 0 {
		o.SnapshotTimeout = 2 * time.Second
	}
}

func (o Options) validate() error {
	if o.TopK < 1 || o.TopK > memsnapshot.MaxMemoryObjectEntries {
		return fmt.Errorf("snapshot top-K must be in [1, %d], got %d", memsnapshot.MaxMemoryObjectEntries, o.TopK)
	}
	if o.SnapshotTimeout < 0 {
		return errors.New("capture timeout must not be negative")
	}
	return nil
}
