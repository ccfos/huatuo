// Copyright 2025, 2026 The HuaTuo Authors
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

package types

// IOHealthEvent is the persisted payload for one IO health event and its
// optional event-triggered evidence. Event identity and trigger time remain in
// the outer tracing document.
type IOHealthEvent struct {
	Type             string              `json:"type"`
	Device           string              `json:"device,omitempty"`
	Array            string              `json:"array,omitempty"`
	Member           string              `json:"member,omitempty"`
	OldState         string              `json:"old_state,omitempty"`
	NewState         string              `json:"new_state,omitempty"`
	NewStateRaw      *uint32             `json:"new_state_raw,omitempty"`
	Operation        string              `json:"operation,omitempty"`
	Status           string              `json:"status,omitempty"`
	Sector           *uint64             `json:"sector,omitempty"`
	CollectionStatus string              `json:"collection_status,omitempty"`
	NVMe             *NVMeHealthEvidence `json:"nvme,omitempty"`
	SCSI             *SCSIHealthEvidence `json:"scsi,omitempty"`
}

// NVMeHealthEvidence contains the bounded fields retained from one
// event-triggered nvme-cli collection.
type NVMeHealthEvidence struct {
	CriticalWarning  *uint8               `json:"critical_warning,omitempty"`
	MediaErrorsTotal *uint64              `json:"media_errors_total,omitempty"`
	ErrorLog         *[]NVMeErrorLogEntry `json:"error_log,omitempty"`
}

// NVMeErrorLogEntry is one retained NVMe Error Information Log entry.
// Fields deliberately do not use omitempty because zero is meaningful.
type NVMeErrorLogEntry struct {
	StatusCodeType uint8  `json:"status_code_type"`
	StatusCode     uint8  `json:"status_code"`
	NSID           uint32 `json:"nsid"`
	LBA            uint64 `json:"lba"`
}

// SCSIHealthEvidence contains the bounded fields retained from one
// event-triggered smartctl collection.
type SCSIHealthEvidence struct {
	SmartPassed          *bool                     `json:"smart_passed,omitempty"`
	InformationException *SCSIInformationException `json:"information_exception,omitempty"`
	Temperature          *SCSITemperature          `json:"temperature,omitempty"`
	GrownDefectCount     *uint64                   `json:"grown_defect_count,omitempty"`
	Read                 *SCSIErrorStats           `json:"read,omitempty"`
	Write                *SCSIErrorStats           `json:"write,omitempty"`
	Verify               *SCSIErrorStats           `json:"verify,omitempty"`
	PendingDefects       *SCSIPendingDefects       `json:"pending_defects,omitempty"`
}

// SCSIInformationException is the ASC/ASCQ pair reported by smartctl.
type SCSIInformationException struct {
	ASC  uint8 `json:"asc"`
	ASCQ uint8 `json:"ascq"`
}

// SCSITemperature is emitted only when both current and trip values exist.
type SCSITemperature struct {
	CurrentCelsius int64 `json:"current_celsius"`
	TripCelsius    int64 `json:"trip_celsius"`
}

// SCSIErrorStats holds one read, write, or verify error-counter group.
type SCSIErrorStats struct {
	GigabytesProcessed       *float64 `json:"gigabytes_processed,omitempty"`
	DelayedCorrections       *uint64  `json:"delayed_corrections,omitempty"`
	RereadRewriteCorrections *uint64  `json:"reread_rewrite_corrections,omitempty"`
	UncorrectedErrors        *uint64  `json:"uncorrected_errors,omitempty"`
}

// SCSIPendingDefects retains the total count and, when supplied by smartctl,
// up to eight representative LBAs. A pointer to an empty slice represents a
// successful empty list and therefore marshals as [] instead of being absent.
type SCSIPendingDefects struct {
	Count      uint64    `json:"count"`
	SampleLBAs *[]uint64 `json:"sample_lbas,omitempty"`
}
