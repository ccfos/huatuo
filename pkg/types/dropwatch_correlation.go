// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

// DropCorrelationIncident is the persistent representation of a dropwatch
// event correlated with NIC hardware and driver evidence.
type DropCorrelationIncident struct {
	EventID              uint64            `json:"event_id"`
	ObservedTimestamp    string            `json:"observed_timestamp"`
	FinalizedTimestamp   string            `json:"finalized_timestamp"`
	HardwareTimestamp    string            `json:"hardware_timestamp,omitempty"`
	Device               string            `json:"device"`
	IfIndex              uint32            `json:"ifindex,omitempty"`
	Driver               string            `json:"driver,omitempty"`
	Direction            string            `json:"direction"`
	Layer                string            `json:"layer"`
	Confidence           float64           `json:"confidence"`
	DropReason           string            `json:"drop_reason"`
	Protocol             string            `json:"protocol,omitempty"`
	ContainerID          string            `json:"container_id,omitempty"`
	PacketLength         uint32            `json:"packet_length,omitempty"`
	CorrelationLagMS     float64           `json:"correlation_lag_ms"`
	Evidence             []string          `json:"evidence"`
	HardwareCounterDelta map[string]uint64 `json:"hardware_counter_delta,omitempty"`
}
