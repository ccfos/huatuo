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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRunEvidenceCommandCapsCombinedOutput(t *testing.T) {
	result := runEvidenceCommand(
		context.Background(),
		"/bin/sh",
		[]string{
			"-c",
			"printf 12345678901234567890; printf abcdefghijklmnopqrst >&2",
		},
		32,
	)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.tooLarge {
		t.Fatal("expected output_too_large")
	}
	if got := len(result.stdout) + len(result.stderr); got != 32 {
		t.Fatalf("captured bytes = %d, want 32", got)
	}
}

func TestParseNVMeEvidencePreservesPresenceAndFiltersErrorLog(t *testing.T) {
	smart, valid, complete := parseNVMeSMART([]byte(
		`{"critical_warning":0,"media_errors":"12"}`,
	))
	if !valid || !complete {
		t.Fatalf("smart valid=%t complete=%t, want true/true", valid, complete)
	}
	if smart.CriticalWarning == nil || *smart.CriticalWarning != 0 {
		t.Fatalf("critical_warning = %#v, want present zero", smart.CriticalWarning)
	}
	if smart.MediaErrorsTotal == nil || *smart.MediaErrorsTotal != 12 {
		t.Fatalf("media_errors_total = %#v, want 12", smart.MediaErrorsTotal)
	}

	rawEntries := []map[string]any{
		{
			"error_count": 0,
		},
		{
			"error_count":  1,
			"sqid":         0,
			"status_field": 1,
			"nsid":         0,
			"lba":          0,
		},
	}
	for i := 1; i <= 9; i++ {
		rawEntries = append(rawEntries, map[string]any{
			"error_count":  i,
			"sqid":         1,
			"status_field": 0x280,
			"nsid":         3,
			"lba":          i,
		})
	}
	data, err := json.Marshal(map[string]any{"errors": rawEntries})
	if err != nil {
		t.Fatal(err)
	}

	errorLog, valid, complete := parseNVMeErrorLog(data)
	if !valid || !complete {
		t.Fatalf("error log valid=%t complete=%t, want true/true", valid, complete)
	}
	if len(errorLog) != maxEvidenceEntries {
		t.Fatalf("error log entries = %d, want %d", len(errorLog), maxEvidenceEntries)
	}
	if errorLog[0].LBA != 1 || errorLog[7].LBA != 8 {
		t.Fatalf("error log order/cap = %#v", errorLog)
	}
	if errorLog[0].StatusCodeType != 2 || errorLog[0].StatusCode != 0x80 {
		t.Fatalf("decoded status = %#v, want SCT=2 SC=0x80", errorLog[0])
	}
}

func TestParseNVMeErrorLogRejectsAllMalformedEntries(t *testing.T) {
	entries, valid, complete := parseNVMeErrorLog([]byte(
		`{"errors":[{"error_count":1,"status_field":640}]}`,
	))
	if valid || complete || len(entries) != 0 {
		t.Fatalf(
			"entries=%#v valid=%t complete=%t, want no trusted evidence",
			entries,
			valid,
			complete,
		)
	}
}

func TestParseNVMeErrorLogSupportsNVMeCLI116And216(t *testing.T) {
	// nvme-cli 1.16 and 2.16 expose the same Error Information Log fields.
	// In particular, neither version emits an opcode.
	fixtures := map[string]string{
		"1.16": `{"errors":[{
			"error_count":1,"sqid":1,"cmdid":7,"status_field":640,
			"phase_tag":0,"parm_error_location":0,"lba":42,"nsid":3,
			"vs":0,"trtype":0,"cs":0,"trtype_spec_info":0
		}]}`,
		"2.16": `{"errors":[{
			"error_count":2,"sqid":2,"cmdid":8,"status_field":640,
			"phase_tag":1,"parm_error_location":4,"lba":84,"nsid":6,
			"vs":0,"trtype":0,"cs":0,"trtype_spec_info":0
		}]}`,
	}

	for version, fixture := range fixtures {
		t.Run(version, func(t *testing.T) {
			entries, valid, complete := parseNVMeErrorLog([]byte(fixture))
			if !valid || !complete || len(entries) != 1 {
				t.Fatalf(
					"entries=%#v valid=%t complete=%t, want one complete record",
					entries,
					valid,
					complete,
				)
			}
			if entries[0].StatusCodeType != 2 || entries[0].StatusCode != 0x80 {
				t.Fatalf("decoded status = %#v, want SCT=2 SC=0x80", entries[0])
			}
		})
	}
}

func TestParseSCSIHealthUsesOnlyWhitelistedStructuredFields(t *testing.T) {
	data := []byte(`{
		"smartctl":{"exit_status":8},
		"smart_status":{
			"passed":false,
			"scsi":{"asc":93,"ascq":1,"ie_string":"discard me"}
		},
		"temperature":{"current":39,"drive_trip":65},
		"scsi_grown_defect_list":0,
		"scsi_error_counter_log":{
			"read":{
				"errors_corrected_by_eccfast":999,
				"errors_corrected_by_eccdelayed":0,
				"errors_corrected_by_rereads_rewrites":2,
				"gigabytes_processed":"123.500",
				"total_uncorrected_errors":4
			},
			"write":{"total_uncorrected_errors":0}
		},
		"scsi_pending_defects":{
			"count":10,
			"table":[
				null,
				{"lba":11},{"lba":12},{"lba":13},{"lba":14},{"lba":15},
				{"lba":16},{"lba":17},{"lba":18},{"lba":19},{"lba":20}
			]
		}
	}`)

	evidence, valid, complete, exitStatus := parseSCSIHealth(data)
	if !valid || !complete {
		t.Fatalf("SCSI valid=%t complete=%t, want true/true", valid, complete)
	}
	if exitStatus == nil || *exitStatus != 8 {
		t.Fatalf("exit status = %#v, want 8", exitStatus)
	}
	if evidence.SmartPassed == nil || *evidence.SmartPassed {
		t.Fatalf("smart_passed = %#v, want present false", evidence.SmartPassed)
	}
	if evidence.InformationException == nil ||
		evidence.InformationException.ASC != 93 ||
		evidence.InformationException.ASCQ != 1 {
		t.Fatalf("information exception = %#v", evidence.InformationException)
	}
	if evidence.Temperature == nil ||
		evidence.Temperature.CurrentCelsius != 39 ||
		evidence.Temperature.TripCelsius != 65 {
		t.Fatalf("temperature = %#v", evidence.Temperature)
	}
	if evidence.GrownDefectCount == nil || *evidence.GrownDefectCount != 0 {
		t.Fatalf("grown defects = %#v, want present zero", evidence.GrownDefectCount)
	}
	if evidence.Read == nil ||
		evidence.Read.DelayedCorrections == nil ||
		*evidence.Read.DelayedCorrections != 0 ||
		evidence.Read.RereadRewriteCorrections == nil ||
		*evidence.Read.RereadRewriteCorrections != 2 ||
		evidence.Read.UncorrectedErrors == nil ||
		*evidence.Read.UncorrectedErrors != 4 {
		t.Fatalf("read counters = %#v", evidence.Read)
	}
	if evidence.PendingDefects == nil ||
		evidence.PendingDefects.SampleLBAs == nil ||
		len(*evidence.PendingDefects.SampleLBAs) != maxEvidenceEntries {
		t.Fatalf("pending defects = %#v", evidence.PendingDefects)
	}
	if got := (*evidence.PendingDefects.SampleLBAs)[7]; got != 18 {
		t.Fatalf("last retained pending LBA = %d, want 18", got)
	}
}

func TestCollectSCSIAcceptsHealthExitBitsAndUsesWhitelistedCommand(t *testing.T) {
	worker := NewEvidenceWorker(EvidenceWorkerOptions{})
	worker.lookupPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	var command []string
	worker.runCommand = func(
		ctx context.Context,
		executable string,
		args []string,
		maxBytes int,
	) commandExecution {
		command = append([]string(nil), args...)
		return commandExecution{
			stdout: []byte(
				`{"smartctl":{"exit_status":8},"smart_status":{"passed":false}}`,
			),
			exitCode: 8,
			err:      errors.New("smartctl reported failed health"),
		}
	}

	evidence, reasons := worker.collectSCSI(context.Background(), "sda")
	if evidence == nil || evidence.SmartPassed == nil || *evidence.SmartPassed {
		t.Fatalf("SCSI evidence = %#v, want present failed health", evidence)
	}
	if len(reasons) != 0 {
		t.Fatalf("collection reasons = %v, want none", reasons)
	}
	wantCommand := []string{
		"--health",
		"--attributes",
		"--log=error",
		"--json=c",
		"/dev/sda",
	}
	if !reflect.DeepEqual(command, wantCommand) {
		t.Fatalf("smartctl command = %q, want %q", command, wantCommand)
	}
}
