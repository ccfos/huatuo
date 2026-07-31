/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package events

import (
	"testing"

	"huatuo-bamai/internal/bpf/abi"
)

func TestOOMPageCountsToBytes(t *testing.T) {
	event := abi.OOMEvent{
		VictimRssAnonPages:  2,
		VictimRssFilePages:  3,
		VictimRssShmemPages: 4,
		VictimTotalVmPages:  5,
	}
	actor := buildTracingData(&event, nil, nil).Victim

	if actor.RssAnonBytes != 2*pageSize ||
		actor.RssFileBytes != 3*pageSize ||
		actor.RssShmemBytes != 4*pageSize ||
		actor.TotalVmBytes != 5*pageSize {
		t.Fatalf("memory footprint = %#v", actor)
	}
}
