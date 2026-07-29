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

import (
	"encoding/json"
	"testing"
)

func TestIOHealthEventPreservesZeroValuesAndEmptyLists(t *testing.T) {
	zeroWarning := uint8(0)
	zeroMediaErrors := uint64(0)
	emptyErrorLog := []NVMeErrorLogEntry{}
	sector := uint64(0)
	event := IOHealthEvent{
		Type:             "nvme_timeout",
		Device:           "nvme0n1",
		Sector:           &sector,
		CollectionStatus: "ok",
		NVMe: &NVMeHealthEvidence{
			CriticalWarning:  &zeroWarning,
			MediaErrorsTotal: &zeroMediaErrors,
			ErrorLog:         &emptyErrorLog,
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if fields["sector"] != float64(0) {
		t.Fatalf("sector = %#v, want explicit zero", fields["sector"])
	}
	nvme, ok := fields["nvme"].(map[string]any)
	if !ok {
		t.Fatalf("nvme evidence missing: %s", data)
	}
	if nvme["critical_warning"] != float64(0) || nvme["media_errors_total"] != float64(0) {
		t.Fatalf("zero-valued NVMe evidence was lost: %#v", nvme)
	}
	errorLog, ok := nvme["error_log"].([]any)
	if !ok || len(errorLog) != 0 {
		t.Fatalf("error_log = %#v, want []", nvme["error_log"])
	}

	var decoded IOHealthEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal into struct failed: %v", err)
	}
	if decoded.Sector == nil || decoded.NVMe == nil ||
		decoded.NVMe.CriticalWarning == nil || decoded.NVMe.MediaErrorsTotal == nil ||
		decoded.NVMe.ErrorLog == nil || len(*decoded.NVMe.ErrorLog) != 0 {
		t.Fatalf("health event did not preserve field presence: %+v", decoded)
	}
}

func TestIOHealthSCSIEventPreservesFalseZeroAndEmptyList(t *testing.T) {
	smartPassed := false
	grownDefects := uint64(0)
	processedGB := float64(0)
	delayedCorrections := uint64(0)
	emptyLBAs := []uint64{}
	event := IOHealthEvent{
		Type:             "scsi_timeout",
		Device:           "sda",
		CollectionStatus: "ok",
		SCSI: &SCSIHealthEvidence{
			SmartPassed:      &smartPassed,
			GrownDefectCount: &grownDefects,
			Read: &SCSIErrorStats{
				GigabytesProcessed: &processedGB,
				DelayedCorrections: &delayedCorrections,
			},
			PendingDefects: &SCSIPendingDefects{
				Count:      0,
				SampleLBAs: &emptyLBAs,
			},
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	scsi, ok := fields["scsi"].(map[string]any)
	if !ok {
		t.Fatalf("SCSI evidence missing: %s", data)
	}
	if scsi["smart_passed"] != false || scsi["grown_defect_count"] != float64(0) {
		t.Fatalf("false/zero-valued SCSI evidence was lost: %#v", scsi)
	}
	read, ok := scsi["read"].(map[string]any)
	if !ok ||
		read["gigabytes_processed"] != float64(0) ||
		read["delayed_corrections"] != float64(0) {
		t.Fatalf("zero-valued SCSI read counters were lost: %#v", scsi["read"])
	}
	pending, ok := scsi["pending_defects"].(map[string]any)
	if !ok || pending["count"] != float64(0) {
		t.Fatalf("pending defects = %#v, want explicit zero count", scsi["pending_defects"])
	}
	sampleLBAs, ok := pending["sample_lbas"].([]any)
	if !ok || len(sampleLBAs) != 0 {
		t.Fatalf("sample_lbas = %#v, want []", pending["sample_lbas"])
	}

	var decoded IOHealthEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal into struct failed: %v", err)
	}
	if decoded.SCSI == nil ||
		decoded.SCSI.SmartPassed == nil || *decoded.SCSI.SmartPassed ||
		decoded.SCSI.GrownDefectCount == nil || *decoded.SCSI.GrownDefectCount != 0 ||
		decoded.SCSI.Read == nil ||
		decoded.SCSI.Read.GigabytesProcessed == nil ||
		decoded.SCSI.Read.DelayedCorrections == nil ||
		decoded.SCSI.PendingDefects == nil ||
		decoded.SCSI.PendingDefects.SampleLBAs == nil ||
		len(*decoded.SCSI.PendingDefects.SampleLBAs) != 0 {
		t.Fatalf("SCSI health event did not preserve field presence: %+v", decoded)
	}
}

func TestIOHealthTransitionOmitsEvidenceFields(t *testing.T) {
	event := IOHealthEvent{
		Type:     "md_member_state",
		Array:    "md0",
		Member:   "sdb",
		OldState: "in_sync",
		NewState: "faulty",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if fields["array"] != "md0" || fields["member"] != "sdb" ||
		fields["old_state"] != "in_sync" || fields["new_state"] != "faulty" {
		t.Fatalf("transition fields = %#v", fields)
	}
	for _, field := range []string{"device", "collection_status", "nvme", "scsi"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("empty %s was serialized: %s", field, data)
		}
	}
}

func TestIOHealthNVMeTransitionPreservesRawState(t *testing.T) {
	raw := uint32(0)
	event := IOHealthEvent{
		Type:        "nvme_state_change",
		Device:      "nvme0",
		NewState:    "new",
		NewStateRaw: &raw,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if fields["new_state"] != "new" || fields["new_state_raw"] != float64(0) {
		t.Fatalf("NVMe state fields = %#v", fields)
	}
}
