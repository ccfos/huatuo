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

import (
	"time"

	"huatuo-bamai/pkg/types"
)

type EvidenceProtocol string

const (
	EvidenceProtocolNVMe EvidenceProtocol = "nvme"
	EvidenceProtocolSCSI EvidenceProtocol = "scsi"

	CollectionReasonTargetUnresolved  = "target_unresolved"
	CollectionReasonToolUnavailable   = "tool_unavailable"
	CollectionReasonTargetUnsupported = "target_unsupported"
	CollectionReasonTimeout           = "timeout"
	CollectionReasonExecError         = "exec_error"
	CollectionReasonOutputTooLarge    = "output_too_large"
	CollectionReasonParseError        = "parse_error"
)

// EvidenceRequest describes one event-triggered health collection.
type EvidenceRequest struct {
	Trigger     types.IOHealthEvent
	Target      string
	Protocol    EvidenceProtocol
	TriggeredAt time.Time
	Reason      string
}

// EvidenceResult contains one bounded health collection result.
type EvidenceResult struct {
	Target      string
	TriggeredAt time.Time
	Event       types.IOHealthEvent
	Reasons     []string
}

// EvidenceWorker holds the dependencies used by one bounded collection.
// Queue ownership and event scheduling are added separately.
type EvidenceWorker struct {
	lookupPath     func(string) (string, error)
	runCommand     commandRunner
	commandTimeout time.Duration
	maxOutputBytes int
}
