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

package health

import "time"

const (
	MDFieldSyncAction = "sync_action"
	MDFieldDegraded   = "degraded"
)

// MDChange describes one observed MD state transition. Member is empty for
// array-level fields.
type MDChange struct {
	Array      string
	Member     string
	Field      string
	OldState   string
	NewState   string
	ObservedAt time.Time
}
