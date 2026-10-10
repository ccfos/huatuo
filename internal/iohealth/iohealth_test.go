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

// Tests cover bounded evidence requests and MD change notifications.
package iohealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

// Bounded command output and protocol parsing.

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

func TestEvidenceWorkerCommandResults(t *testing.T) {
	execFailure := commandExecution{exitCode: -1, err: errors.New("command failed")}
	criticalWarning := uint8(0)
	mediaErrors := uint64(12)
	emptyErrorLog := []types.NVMeErrorLogEntry{}
	failedHealth := false

	for _, test := range []struct {
		name         string
		protocol     EvidenceProtocol
		lookupErr    error
		executions   []commandExecution
		timeoutFirst bool
		wantStatus   string
		wantReasons  []string
		wantNVMe     *types.NVMeHealthEvidence
		wantSCSI     *types.SCSIHealthEvidence
	}{
		{
			name:        "nvme tool missing",
			protocol:    EvidenceProtocolNVMe,
			lookupErr:   errors.New("nvme not found"),
			wantStatus:  "unsupported",
			wantReasons: []string{CollectionReasonToolUnavailable},
		},
		{
			name:        "scsi tool missing",
			protocol:    EvidenceProtocolSCSI,
			lookupErr:   errors.New("smartctl not found"),
			wantStatus:  "unsupported",
			wantReasons: []string{CollectionReasonToolUnavailable},
		},
		{
			name:        "scsi execution failure",
			protocol:    EvidenceProtocolSCSI,
			executions:  []commandExecution{execFailure},
			wantStatus:  "error",
			wantReasons: []string{CollectionReasonExecError},
		},
		{
			name:         "scsi command deadline",
			protocol:     EvidenceProtocolSCSI,
			executions:   []commandExecution{{}},
			timeoutFirst: true,
			wantStatus:   "timeout",
			wantReasons:  []string{CollectionReasonTimeout},
		},
		{
			name:     "nvme deadline leaves next command usable",
			protocol: EvidenceProtocolNVMe,
			executions: []commandExecution{
				{},
				{stdout: []byte(`{"errors":[]}`)},
			},
			timeoutFirst: true,
			wantStatus:   "partial",
			wantReasons:  []string{CollectionReasonTimeout},
			wantNVMe:     &types.NVMeHealthEvidence{ErrorLog: &emptyErrorLog},
		},
		{
			name:        "nvme execution failures deduplicate reason",
			protocol:    EvidenceProtocolNVMe,
			executions:  []commandExecution{execFailure, execFailure},
			wantStatus:  "error",
			wantReasons: []string{CollectionReasonExecError},
		},
		{
			name:     "nvme retains smart evidence after error-log failure",
			protocol: EvidenceProtocolNVMe,
			executions: []commandExecution{
				{stdout: []byte(`{"critical_warning":0,"media_errors":12}`)},
				execFailure,
			},
			wantStatus:  "partial",
			wantReasons: []string{CollectionReasonExecError},
			wantNVMe: &types.NVMeHealthEvidence{
				CriticalWarning:  &criticalWarning,
				MediaErrorsTotal: &mediaErrors,
			},
		},
		{
			name:     "scsi retains complete evidence with execution error",
			protocol: EvidenceProtocolSCSI,
			executions: []commandExecution{{
				stdout:   []byte(`{"smart_status":{"passed":false}}`),
				exitCode: 2,
				err:      errors.New("smartctl device access failure"),
			}},
			wantStatus:  "partial",
			wantReasons: []string{CollectionReasonExecError},
			wantSCSI:    &types.SCSIHealthEvidence{SmartPassed: &failedHealth},
		},
		{
			name:     "scsi retains partial evidence with reported execution error",
			protocol: EvidenceProtocolSCSI,
			executions: []commandExecution{{
				stdout: []byte(`{"smartctl":{"exit_status":2},` +
					`"smart_status":{"passed":false},"scsi_grown_defect_list":"invalid"}`),
			}},
			wantStatus:  "partial",
			wantReasons: []string{CollectionReasonParseError, CollectionReasonExecError},
			wantSCSI:    &types.SCSIHealthEvidence{SmartPassed: &failedHealth},
		},
		{
			name:     "scsi health exit bits are successful evidence",
			protocol: EvidenceProtocolSCSI,
			executions: []commandExecution{{
				stdout:   []byte(`{"smartctl":{"exit_status":8},"smart_status":{"passed":false}}`),
				exitCode: 8,
				err:      errors.New("smartctl reported failed health"),
			}},
			wantStatus: "ok",
			wantSCSI:   &types.SCSIHealthEvidence{SmartPassed: &failedHealth},
		},
		{
			name:     "scsi oversized output is not trusted",
			protocol: EvidenceProtocolSCSI,
			executions: []commandExecution{{
				stdout:   []byte(`{"smart_status":{"passed":false}}`),
				tooLarge: true,
			}},
			wantStatus:  "error",
			wantReasons: []string{CollectionReasonOutputTooLarge},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan EvidenceResult, 2)
			worker := NewEvidenceWorker(EvidenceWorkerOptions{
				OnResult: func(result EvidenceResult) { results <- result },
			})
			worker.lookupPath = func(name string) (string, error) {
				if test.lookupErr != nil {
					return "", test.lookupErr
				}
				return "/usr/bin/" + name, nil
			}
			if test.timeoutFirst {
				worker.commandTimeout = 20 * time.Millisecond
			}
			calls := 0
			worker.runCommand = func(ctx context.Context, _ string, _ []string, _ int) commandExecution {
				calls++
				if calls > len(test.executions) {
					t.Errorf("unexpected command call %d", calls)
					return execFailure
				}
				if test.timeoutFirst && calls == 1 {
					<-ctx.Done()
					return commandExecution{exitCode: -1, err: ctx.Err()}
				}
				return test.executions[calls-1]
			}

			ctx, cancel := context.WithCancel(t.Context())
			worker.Start(ctx)
			done := make(chan struct{})
			go func() {
				worker.Wait()
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("evidence worker did not stop")
				}
			})
			target := "sda"
			if test.protocol == EvidenceProtocolNVMe {
				target = "nvme0"
			}
			request := EvidenceRequest{
				Trigger:     types.IOHealthEvent{Type: "block_error", Device: target},
				Target:      target,
				Protocol:    test.protocol,
				TriggeredAt: time.Unix(50, 0),
			}
			if !worker.Submit(request) {
				t.Fatal("evidence request was not accepted")
			}
			result := receiveEvidenceResult(t, results)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("evidence worker did not stop after callback")
			}
			wantEvent := request.Trigger
			wantEvent.CollectionStatus = test.wantStatus
			wantEvent.NVMe = test.wantNVMe
			wantEvent.SCSI = test.wantSCSI
			if !reflect.DeepEqual(result.Event, wantEvent) ||
				!reflect.DeepEqual(result.Reasons, test.wantReasons) ||
				result.Target != target || !result.TriggeredAt.Equal(request.TriggeredAt) {
				t.Fatalf("result = %#v, want event=%#v reasons=%v", result, wantEvent, test.wantReasons)
			}
			if calls != len(test.executions) {
				t.Fatalf("command calls = %d, want %d", calls, len(test.executions))
			}
			if len(results) != 0 {
				t.Fatal("one accepted request produced duplicate callbacks")
			}
		})
	}
}

// Serialized requests, cooldowns, and generation isolation.

func TestEvidenceWorkerSerializesDeduplicatesAndAppliesCooldown(t *testing.T) {
	clock := &fakeEvidenceClock{now: time.Unix(100, 0)}
	results := make(chan EvidenceResult, 3)
	started := make(chan string, 8)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32

	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.now = clock.Now
	worker.lookupPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	worker.commandTimeout = time.Hour
	worker.runCommand = func(
		ctx context.Context,
		executable string,
		args []string,
		maxBytes int,
	) commandExecution {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- args[len(args)-1]
		if calls.Add(1) == 1 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return commandExecution{exitCode: -1, err: ctx.Err()}
			}
		}
		switch args[0] {
		case "smart-log":
			return commandExecution{
				stdout:   []byte(`{"critical_warning":0,"media_errors":0}`),
				exitCode: 0,
			}
		case "error-log":
			return commandExecution{
				stdout:   []byte(`{"errors":[]}`),
				exitCode: 0,
			}
		default:
			return commandExecution{exitCode: -1, err: errors.New("unexpected command")}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)

	request := func(target string) EvidenceRequest {
		return EvidenceRequest{
			Trigger: types.IOHealthEvent{
				Type:   "nvme_timeout",
				Device: target + "n1",
			},
			Target:      target,
			Protocol:    EvidenceProtocolNVMe,
			TriggeredAt: time.Unix(50, 0),
		}
	}
	if !worker.Submit(request("nvme0")) {
		t.Fatal("first nvme0 trigger was not accepted")
	}
	if got := <-started; got != "/dev/nvme0" {
		t.Fatalf("first command target = %q, want /dev/nvme0", got)
	}
	if worker.Submit(request("nvme0")) {
		t.Fatal("running nvme0 trigger should have been merged")
	}
	if !worker.Submit(request("nvme1")) {
		t.Fatal("nvme1 trigger was not accepted")
	}
	if worker.Submit(request("nvme1")) {
		t.Fatal("pending nvme1 trigger should have been merged")
	}
	close(releaseFirst)

	first := receiveEvidenceResult(t, results)
	second := receiveEvidenceResult(t, results)
	if first.Target != "nvme0" || second.Target != "nvme1" {
		t.Fatalf("result order = %q, %q", first.Target, second.Target)
	}
	if first.Event.CollectionStatus != "ok" ||
		second.Event.CollectionStatus != "ok" {
		t.Fatalf("collection statuses = %q, %q", first.Event.CollectionStatus, second.Event.CollectionStatus)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent commands = %d, want 1", maximum.Load())
	}
	if worker.Submit(request("nvme0")) {
		t.Fatal("nvme0 trigger inside cooldown should not be accepted")
	}

	clock.Advance(evidenceCooldown)
	if !worker.Submit(request("nvme0")) {
		t.Fatal("nvme0 trigger after cooldown was not accepted")
	}
	if result := receiveEvidenceResult(t, results); result.Target != "nvme0" {
		t.Fatalf("post-cooldown result target = %q, want nvme0", result.Target)
	}

	cancel()
	worker.Wait()
}

func TestEvidenceWorkerSeparatesReusedTargetGenerations(t *testing.T) {
	clock := &fakeEvidenceClock{now: time.Unix(100, 0)}
	results := make(chan EvidenceResult, 3)
	started := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32

	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.now = clock.Now
	worker.lookupPath = func(string) (string, error) {
		return "/usr/bin/smartctl", nil
	}
	worker.runCommand = func(
		ctx context.Context,
		_ string,
		_ []string,
		_ int,
	) commandExecution {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return commandExecution{exitCode: -1, err: ctx.Err()}
			}
		}
		return commandExecution{
			stdout:   []byte(`{"smart_status":{"passed":true}}`),
			exitCode: 0,
		}
	}

	request := func(identity string) EvidenceRequest {
		return EvidenceRequest{
			Trigger:  types.IOHealthEvent{Type: "block_error", Device: "sda"},
			Target:   "sda",
			Identity: identity,
			Protocol: EvidenceProtocolSCSI,
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	if !worker.Submit(request("generation-1")) {
		t.Fatal("first generation was not accepted")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first generation did not start")
	}
	if !worker.Submit(request("generation-2")) {
		t.Fatal("replacement generation inherited old inflight state")
	}
	if worker.Submit(request("generation-2")) {
		t.Fatal("duplicate replacement generation was not merged")
	}
	close(releaseFirst)
	receiveEvidenceResult(t, results)
	receiveEvidenceResult(t, results)
	if worker.Submit(request("generation-2")) {
		t.Fatal("replacement generation bypassed its own cooldown")
	}

	cancel()
	worker.Wait()
}

func TestEvidenceWorkerStaleGenerationDoesNotReplaceCurrentCooldown(t *testing.T) {
	results := make(chan EvidenceResult, 2)
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
		ValidateIdentity: func(request EvidenceRequest) bool {
			return request.Identity == "generation-2"
		},
	})
	worker.lookupPath = func(string) (string, error) {
		return "/usr/bin/smartctl", nil
	}
	worker.runCommand = func(
		context.Context,
		string,
		[]string,
		int,
	) commandExecution {
		return commandExecution{
			stdout:   []byte(`{"smart_status":{"passed":true}}`),
			exitCode: 0,
		}
	}
	request := func(identity string) EvidenceRequest {
		return EvidenceRequest{
			Trigger:  types.IOHealthEvent{Type: "block_error", Device: "sda"},
			Target:   "sda",
			Identity: identity,
			Protocol: EvidenceProtocolSCSI,
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	if !worker.Submit(request("generation-2")) {
		t.Fatal("current generation was not accepted")
	}
	if result := receiveEvidenceResult(t, results); result.Event.CollectionStatus != "ok" {
		t.Fatalf("current-generation result = %#v", result)
	}
	if !worker.Submit(request("generation-1")) {
		t.Fatal("stale generation was not accepted for identity validation")
	}
	result := receiveEvidenceResult(t, results)
	if result.Event.CollectionStatus != "unsupported" ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetChanged {
		t.Fatalf("stale-generation result = %#v", result)
	}
	if worker.Submit(request("generation-2")) {
		t.Fatal("stale generation replaced the current generation cooldown")
	}
	if !worker.Submit(EvidenceRequest{
		Trigger: types.IOHealthEvent{Type: "block_error", Device: "sda"},
		Reason:  CollectionReasonTargetUnresolved,
	}) {
		t.Fatal("unresolved request was not accepted for reporting")
	}
	result = receiveEvidenceResult(t, results)
	if result.Event.CollectionStatus != "unsupported" ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetUnresolved {
		t.Fatalf("unresolved result = %#v", result)
	}
	if worker.Submit(request("generation-2")) {
		t.Fatal("unresolved request replaced the current generation cooldown")
	}

	cancel()
	worker.Wait()
}

func TestEvidenceWorkerBoundsDistinctGenerationQueue(t *testing.T) {
	started := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	results := make(chan EvidenceResult, 2)
	var calls atomic.Int32
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.queueCapacity = 1
	worker.lookupPath = func(string) (string, error) {
		return "/usr/bin/smartctl", nil
	}
	worker.runCommand = func(
		ctx context.Context,
		_ string,
		_ []string,
		_ int,
	) commandExecution {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return commandExecution{exitCode: -1, err: ctx.Err()}
			}
		}
		return commandExecution{
			stdout:   []byte(`{"smart_status":{"passed":true}}`),
			exitCode: 0,
		}
	}
	request := func(identity string) EvidenceRequest {
		return EvidenceRequest{
			Trigger:  types.IOHealthEvent{Type: "block_error", Device: "sda"},
			Target:   "sda",
			Identity: identity,
			Protocol: EvidenceProtocolSCSI,
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	if !worker.Submit(request("generation-1")) {
		t.Fatal("running generation was not accepted")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("running generation did not start")
	}
	if !worker.Submit(request("generation-2")) {
		t.Fatal("queued generation was not accepted")
	}
	if worker.Submit(request("generation-3")) {
		t.Fatal("distinct-generation queue exceeded its capacity")
	}

	close(releaseFirst)
	receiveEvidenceResult(t, results)
	receiveEvidenceResult(t, results)
	cancel()
	worker.Wait()
}

func TestEvidenceWorkerReportsUnsupportedAttemptWithoutCommand(t *testing.T) {
	results := make(chan EvidenceResult, 1)
	var commandCalled atomic.Bool
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
	})
	worker.runCommand = func(
		context.Context,
		string,
		[]string,
		int,
	) commandExecution {
		commandCalled.Store(true)
		return commandExecution{exitCode: -1, err: errors.New("unexpected command")}
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	if !worker.Submit(EvidenceRequest{
		Trigger: types.IOHealthEvent{
			Type:   "block_error",
			Device: "dm-0",
		},
		Protocol: EvidenceProtocolSCSI,
		Reason:   CollectionReasonTargetUnsupported,
	}) {
		t.Fatal("unsupported attempt was not accepted")
	}
	result := receiveEvidenceResult(t, results)
	if result.Target != "dm-0" ||
		result.Event.CollectionStatus != "unsupported" ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetUnsupported {
		t.Fatalf("unsupported result = %#v", result)
	}
	if commandCalled.Load() {
		t.Fatal("unsupported attempt executed a command")
	}
	cancel()
	worker.Wait()
}

func TestEvidenceWorkerRejectsReusedTargetBeforeCommand(t *testing.T) {
	results := make(chan EvidenceResult, 1)
	var commandCalled atomic.Bool
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
		ValidateIdentity: func(EvidenceRequest) bool {
			return false
		},
	})
	worker.runCommand = func(
		context.Context,
		string,
		[]string,
		int,
	) commandExecution {
		commandCalled.Store(true)
		return commandExecution{exitCode: -1, err: errors.New("unexpected command")}
	}

	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	if !worker.Submit(EvidenceRequest{
		Trigger: types.IOHealthEvent{
			Type:   "block_error",
			Device: "sda",
		},
		Target:      "sda",
		Identity:    "8:0/diskseq=1",
		Protocol:    EvidenceProtocolSCSI,
		TriggeredAt: time.Unix(1, 0),
	}) {
		t.Fatal("identity-qualified request was not accepted")
	}
	result := receiveEvidenceResult(t, results)
	if result.Event.CollectionStatus != "unsupported" ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetChanged {
		t.Fatalf("reused-target result = %#v", result)
	}
	if commandCalled.Load() {
		t.Fatal("reused target executed an evidence command")
	}
	cancel()
	worker.Wait()
}

func TestEvidenceWorkerRejectsTargetChangedDuringCommand(t *testing.T) {
	results := make(chan EvidenceResult, 1)
	var validations atomic.Int32
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) {
			results <- result
		},
		ValidateIdentity: func(EvidenceRequest) bool {
			return validations.Add(1) == 1
		},
	})
	worker.lookupPath = func(string) (string, error) {
		return "/usr/bin/smartctl", nil
	}
	worker.runCommand = func(
		context.Context,
		string,
		[]string,
		int,
	) commandExecution {
		return commandExecution{
			stdout:   []byte(`{"smart_status":{"passed":true}}`),
			exitCode: 0,
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	if !worker.Submit(EvidenceRequest{
		Trigger:  types.IOHealthEvent{Type: "block_error", Device: "sda"},
		Target:   "sda",
		Identity: "old-generation",
		Protocol: EvidenceProtocolSCSI,
	}) {
		t.Fatal("identity-qualified request was not accepted")
	}
	result := receiveEvidenceResult(t, results)
	if result.Event.CollectionStatus != "unsupported" ||
		result.Event.SCSI != nil ||
		len(result.Reasons) != 1 ||
		result.Reasons[0] != CollectionReasonTargetChanged {
		t.Fatalf("changed-during-command result = %#v", result)
	}
	cancel()
	worker.Wait()
}

func TestEvidenceWorkerCancellationDrainsAcceptedRequests(t *testing.T) {
	results := make(chan EvidenceResult, 3)
	started := make(chan struct{}, 1)
	worker := NewEvidenceWorker(EvidenceWorkerOptions{
		OnResult: func(result EvidenceResult) { results <- result },
	})
	worker.lookupPath = func(string) (string, error) {
		return "/usr/bin/smartctl", nil
	}
	worker.commandTimeout = time.Hour
	worker.runCommand = func(ctx context.Context, _ string, _ []string, _ int) commandExecution {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return commandExecution{exitCode: -1, err: ctx.Err()}
	}

	ctx, cancel := context.WithCancel(t.Context())
	worker.Start(ctx)
	done := make(chan struct{})
	go func() {
		worker.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("evidence worker did not stop")
		}
	})
	request := func(target string) EvidenceRequest {
		return EvidenceRequest{
			Trigger:     types.IOHealthEvent{Type: "block_error", Device: target},
			Target:      target,
			Protocol:    EvidenceProtocolSCSI,
			TriggeredAt: time.Unix(50, 0),
		}
	}
	if !worker.Submit(request("sda")) {
		t.Fatal("running request was not accepted")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first evidence command did not start")
	}
	if !worker.Submit(request("sdb")) {
		t.Fatal("queued request was not accepted")
	}

	cancel()
	if worker.Submit(request("sdc")) {
		t.Fatal("request was accepted after cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the running command and drain the queue")
	}
	if got := len(results); got != 2 {
		t.Fatalf("callback count = %d, want one per accepted request (2)", got)
	}
	for _, target := range []string{"sda", "sdb"} {
		result := receiveEvidenceResult(t, results)
		if result.Target != target || result.Event.Device != target ||
			result.Event.Type != "block_error" ||
			!result.TriggeredAt.Equal(time.Unix(50, 0)) ||
			result.Event.CollectionStatus != "error" ||
			result.Event.NVMe != nil || result.Event.SCSI != nil ||
			len(result.Reasons) != 1 || result.Reasons[0] != CollectionReasonExecError {
			t.Fatalf("canceled result for %s = %#v", target, result)
		}
	}
	if worker.Submit(request("sdc")) {
		t.Fatal("request was accepted after worker exit")
	}
}

type fakeEvidenceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeEvidenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeEvidenceClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func receiveEvidenceResult(
	t *testing.T,
	results <-chan EvidenceResult,
) EvidenceResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for evidence result")
		return EvidenceResult{}
	}
}

// MD baseline, topology, and change notifications.

type mdFixture struct {
	mdstat   string
	sysBlock string
}

type mdPollResult struct {
	fd     int32
	events int16
	err    error
}

type fakeMDPoller struct {
	results chan mdPollResult
}

func newFakeMDPoller() *fakeMDPoller {
	return &fakeMDPoller{results: make(chan mdPollResult, 16)}
}

func (p *fakeMDPoller) poll(fds []unix.PollFd, _ int) (int, error) {
	result := <-p.results
	if result.err != nil {
		return 0, result.err
	}
	for i := range fds {
		if fds[i].Fd == result.fd {
			fds[i].Revents = result.events
			return 1, nil
		}
	}
	return 0, unix.EINTR
}

func (p *fakeMDPoller) trigger(fd int32) {
	p.results <- mdPollResult{fd: fd, events: unix.POLLPRI | unix.POLLERR}
}

func (p *fakeMDPoller) fail(err error) {
	p.results <- mdPollResult{err: err}
}

func setupMDFixture(t *testing.T, devices map[string]map[string]string) mdFixture {
	t.Helper()
	root := t.TempDir()
	mdstat := filepath.Join(root, "proc", "mdstat")
	sysBlock := filepath.Join(root, "sys", "block")
	if err := os.MkdirAll(filepath.Dir(mdstat), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sysBlock, 0o755); err != nil {
		t.Fatal(err)
	}
	for device, files := range devices {
		writeMDDevice(t, sysBlock, device, files)
	}
	deviceNames := make([]string, 0, len(devices))
	for device := range devices {
		deviceNames = append(deviceNames, device)
	}
	writeMDStatFixture(t, mdstat, deviceNames...)
	return mdFixture{mdstat: mdstat, sysBlock: sysBlock}
}

func writeMDStatFixture(t *testing.T, path string, devices ...string) {
	writeMDStatFixtureForLevel(t, path, "raid1", devices...)
}

func writeMDStatFixtureForLevel(
	t *testing.T,
	path, level string,
	devices ...string,
) {
	t.Helper()
	sort.Strings(devices)
	var content strings.Builder
	fmt.Fprintf(&content, "Personalities : [%s]\n", level)
	for _, device := range devices {
		fmt.Fprintf(&content, "%s : active %s\n", device, level)
	}
	content.WriteString("unused devices: <none>\n")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeMDDevice(t *testing.T, sysBlock, device string, files map[string]string) {
	t.Helper()
	mdDir := filepath.Join(sysBlock, device, "md")
	for name, content := range files {
		path := filepath.Join(mdDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func startMDWatcher(t *testing.T, fixture mdFixture) (*MDWatcher, *fakeMDPoller, context.CancelFunc) {
	t.Helper()
	poller := newFakeMDPoller()
	watcher := newMDWatcher(fixture.mdstat, fixture.sysBlock, poller.poll)
	ctx, cancel := context.WithCancel(context.Background())
	if err := watcher.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start() error = %v", err)
	}
	return watcher, poller, cancel
}

func stopMDWatcher(
	t *testing.T,
	watcher *MDWatcher,
	poller *fakeMDPoller,
	cancel context.CancelFunc,
) {
	t.Helper()
	watcher.lifecycleMu.Lock()
	wakeRead := watcher.wakeRead
	watcher.lifecycleMu.Unlock()
	cancel()
	poller.trigger(int32(wakeRead))
	// Failure-path tests assert the run error before deferred cleanup.
	_ = watcher.Wait()
}

func mdFileFD(t *testing.T, watcher *MDWatcher, array, member, field string) int32 {
	t.Helper()
	return int32(mdFileTarget(t, watcher, array, member, field).file.Fd())
}

func mdFileTarget(
	t *testing.T,
	watcher *MDWatcher,
	array, member, field string,
) *mdWatchFile {
	t.Helper()
	watcher.mu.RLock()
	defer watcher.mu.RUnlock()
	for _, target := range watcher.files {
		if target.array == array && target.member == member && target.field == field {
			return target
		}
	}
	t.Fatalf("watch %s/%s/%s not found", array, member, field)
	return nil
}

func handleMDWatchEvent(t *testing.T, watcher *MDWatcher, target *mdWatchFile) {
	t.Helper()
	if err := watcher.handleEvent(target, unix.POLLPRI|unix.POLLERR); err != nil {
		t.Fatalf("handleEvent() error = %v", err)
	}
}

func waitMDChange(t *testing.T, watcher *MDWatcher, match func(MDChange) bool) MDChange {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case change := <-watcher.Changes():
			if match(change) {
				return change
			}
		case <-deadline:
			t.Fatal("timed out waiting for MD change")
		}
	}
}

func assertNoMDChange(t *testing.T, watcher *MDWatcher) {
	t.Helper()
	select {
	case change := <-watcher.Changes():
		t.Fatalf("unexpected MD change: %+v", change)
	default:
	}
}

func assertMDMemberWatchPreserved(
	t *testing.T,
	watcher *MDWatcher,
	previous *mdWatchFile,
	array, member, state string,
) {
	t.Helper()

	current := mdFileTarget(t, watcher, array, member, mdMemberState)
	if current != previous {
		t.Fatalf("%s/%s watch was replaced after failed rebuild", array, member)
	}
	watcher.mu.RLock()
	currentState := watcher.members[array][member]
	watcher.mu.RUnlock()
	if currentState != state {
		t.Fatalf("%s/%s state = %q, want %q", array, member, currentState, state)
	}
	value, err := readOpenMDFile(current.file)
	if err != nil {
		t.Fatalf("preserved %s/%s watch cannot be read: %v", array, member, err)
	}
	if value != state {
		t.Fatalf("preserved %s/%s watch value = %q, want %q", array, member, value, state)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsTransitionsAfterInitialBaseline(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	assertNoMDChange(t, watcher)

	syncPath := filepath.Join(fixture.sysBlock, "md0", "md", mdSyncAction)
	if err := os.WriteFile(syncPath, []byte("recover\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdSyncAction))
	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldSyncAction
	})
	if change.Array != "md0" || change.Member != "" ||
		change.OldState != "idle" || change.NewState != "recover" ||
		change.ObservedAt.IsZero() {
		t.Fatalf("sync change = %+v", change)
	}

	memberPath := filepath.Join(fixture.sysBlock, "md0", "md", "dev-sdb", mdMemberState)
	if err := os.WriteFile(memberPath, []byte("faulty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "sdb", mdMemberState))
	change = waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldMemberState
	})
	if change.Array != "md0" || change.Member != "sdb" ||
		change.OldState != "in_sync" || change.NewState != "faulty" ||
		change.ObservedAt.IsZero() {
		t.Fatalf("member change = %+v", change)
	}

	degradedPath := filepath.Join(fixture.sysBlock, "md0", "md", mdDegraded)
	if err := os.WriteFile(degradedPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdDegraded))
	change = waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldDegraded
	})
	if change.Array != "md0" || change.OldState != "0" ||
		change.NewState != "1" {
		t.Fatalf("degraded change = %+v", change)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherStartsWithoutRedundancyAttributes(t *testing.T) {
	for _, level := range []string{"raid0", "linear"} {
		t.Run(level, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    level + "\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
				},
			})
			writeMDStatFixtureForLevel(t, fixture.mdstat, level, "md0")
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sda",
				mdMemberState,
			)
			if err := os.WriteFile(statePath, []byte("faulty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			poller.trigger(mdFileFD(t, watcher, "md0", "sda", mdMemberState))

			change := waitMDChange(t, watcher, func(change MDChange) bool {
				return change.Field == MDFieldMemberState
			})
			if change.Array != "md0" || change.Member != "sda" ||
				change.OldState != "in_sync" || change.NewState != "faulty" {
				t.Fatalf("member change = %+v", change)
			}
		})
	}
}

func TestMDWatcherStopsAfterNotificationRebuildFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		array string
		field string
	}{
		{name: "mdstat", field: mdStatField},
		{name: "level", array: "md0", field: mdLevel},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    "raid1\n",
					mdSyncAction:               "idle\n",
					mdDegraded:                 "0\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
				},
			})
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sda",
				mdMemberState,
			)
			if err := os.Remove(statePath); err != nil {
				t.Fatal(err)
			}
			poller.trigger(mdFileFD(t, watcher, test.array, "", test.field))
			sentinel := errors.New("unexpected second poll")
			poller.fail(sentinel)

			err := watcher.Wait()
			if err == nil {
				t.Fatal("Wait() error = nil, want rebuild error")
			}
			if errors.Is(err, sentinel) {
				t.Fatalf("Wait() error = %v, watcher continued polling", err)
			}
			if !strings.Contains(err.Error(), "rebuild after "+test.field+" notification") {
				t.Fatalf("Wait() error = %v, want notification rebuild context", err)
			}
		})
	}
}

func TestMDWatcherPollsOnlyForPriorityChanges(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:      "raid1\n",
			mdSyncAction: "idle\n",
			mdDegraded:   "0\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	watcher.lifecycleMu.Lock()
	wakeRead := watcher.wakeRead
	watcher.lifecycleMu.Unlock()
	pollFDs, targets := watcher.pollFDs(wakeRead)
	if len(pollFDs) != len(targets) || len(pollFDs) < 2 {
		t.Fatalf("poll set has %d fds and %d targets", len(pollFDs), len(targets))
	}
	if pollFDs[0].Events != unix.POLLIN {
		t.Fatalf("wake pipe events = %#x, want POLLIN", pollFDs[0].Events)
	}
	for i := 1; i < len(pollFDs); i++ {
		if pollFDs[i].Events != unix.POLLPRI {
			t.Fatalf(
				"watch %s events = %#x, want POLLPRI",
				targets[i].field,
				pollFDs[i].Events,
			)
		}
	}
}

func TestMDWatcherRebuildPreservesMemberFaultNotification(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	mdstatTarget := mdFileTarget(t, watcher, "", "", mdStatField)
	memberTarget := mdFileTarget(t, watcher, "md0", "sdb", mdMemberState)
	memberPath := filepath.Join(fixture.sysBlock, "md0", "md", "dev-sdb", mdMemberState)
	if err := os.WriteFile(memberPath, []byte("faulty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A real fault can make mdstat and the member fd ready together. Model
	// mdstat being handled first: rebuild replaces and closes memberTarget,
	// so the state diff must preserve the notification before the stale fd is
	// ignored.
	handleMDWatchEvent(t, watcher, mdstatTarget)
	handleMDWatchEvent(t, watcher, memberTarget)

	select {
	case change := <-watcher.Changes():
		if change.Array != "md0" || change.Member != "sdb" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" || change.NewState != "faulty" {
			t.Fatalf("member change = %+v", change)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rebuilt member change")
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherEmitsEveryMemberFaultSeenWithDegradedChange(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	degradedTarget := mdFileTarget(t, watcher, "md0", "", mdDegraded)
	memberTargets := []*mdWatchFile{
		mdFileTarget(t, watcher, "md0", "sda", mdMemberState),
		mdFileTarget(t, watcher, "md0", "sdb", mdMemberState),
	}
	mdDir := filepath.Join(fixture.sysBlock, "md0", "md")
	for _, member := range []string{"sda", "sdb"} {
		if err := os.WriteFile(
			filepath.Join(mdDir, "dev-"+member, mdMemberState),
			[]byte("faulty\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mdDir, mdDegraded), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handleMDWatchEvent(t, watcher, degradedTarget)
	degraded := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldDegraded
	})
	if degraded.OldState != "0" || degraded.NewState != "2" {
		t.Fatalf("degraded change = %+v", degraded)
	}
	changes := make(map[string]MDChange)
	for range 2 {
		select {
		case change := <-watcher.Changes():
			changes[change.Member] = change
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for both faulted member changes")
		}
	}
	for _, member := range []string{"sda", "sdb"} {
		change, ok := changes[member]
		if !ok || change.Array != "md0" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" || change.NewState != "faulty" {
			t.Fatalf("%s change = %+v, present=%t", member, change, ok)
		}
	}

	// Member fds can already be ready when degraded is processed. Their stale
	// notifications must not duplicate changes whose cache was refreshed above.
	for _, target := range memberTargets {
		handleMDWatchEvent(t, watcher, target)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsRemovedMemberFromActiveArray(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	mdstatTarget := mdFileTarget(t, watcher, "", "", mdStatField)
	mdDir := filepath.Join(fixture.sysBlock, "md0", "md")
	if err := os.RemoveAll(filepath.Join(mdDir, "dev-sdb")); err != nil {
		t.Fatal(err)
	}
	handleMDWatchEvent(t, watcher, mdstatTarget)

	select {
	case change := <-watcher.Changes():
		if change.Array != "md0" || change.Member != "sdb" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" ||
			change.NewState != MDMemberStateRemoved {
			t.Fatalf("removed member change = %+v", change)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for removed member change")
	}

	// Adding a replacement or expansion member is not a health failure.
	writeMDDevice(t, fixture.sysBlock, "md0", map[string]string{
		"dev-sdc/" + mdMemberState: "spare\n",
	})
	mdstatTarget = mdFileTarget(t, watcher, "", "", mdStatField)
	handleMDWatchEvent(t, watcher, mdstatTarget)
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsMemberWriteError(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	statePath := filepath.Join(
		fixture.sysBlock,
		"md0",
		"md",
		"dev-sda",
		mdMemberState,
	)
	if err := os.WriteFile(statePath, []byte("in_sync,write_error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "sda", mdMemberState))

	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldMemberState
	})
	if change.Member != "sda" || change.OldState != "in_sync" ||
		change.NewState != "in_sync,write_error" {
		t.Fatalf("write_error change = %+v", change)
	}
}

func TestMDWatcherRejectsIncompleteArrayBaseline(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		breakField func(*testing.T, string)
	}{
		{
			name:  "unreadable sync action",
			field: mdSyncAction,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "unreadable level",
			field: mdLevel,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "malformed degraded",
			field: mdDegraded,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:      "raid1\n",
					mdSyncAction: "idle\n",
					mdDegraded:   "0\n",
				},
			})
			path := filepath.Join(fixture.sysBlock, "md0", "md", test.field)
			test.breakField(t, path)

			poller := newFakeMDPoller()
			watcher := newMDWatcher(fixture.mdstat, fixture.sysBlock, poller.poll)
			ctx, cancel := context.WithCancel(context.Background())
			err := watcher.Start(ctx)
			if err == nil {
				stopMDWatcher(t, watcher, poller, cancel)
				t.Fatal("Start() error = nil, want incomplete baseline error")
			}
			cancel()
		})
	}
}

func TestMDWatcherRejectsIncompleteMemberStateRebuild(t *testing.T) {
	tests := []struct {
		name       string
		breakState func(*testing.T, string)
	}{
		{
			name: "missing state",
			breakState: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unreadable state",
			breakState: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    "raid1\n",
					mdSyncAction:               "idle\n",
					mdDegraded:                 "0\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
					"dev-sdb/" + mdMemberState: "in_sync\n",
				},
			})
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			previous := mdFileTarget(t, watcher, "md0", "sdb", mdMemberState)
			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sdb",
				mdMemberState,
			)
			test.breakState(t, statePath)

			watcher.mu.Lock()
			err := watcher.rebuildLocked(true)
			watcher.mu.Unlock()
			if err == nil {
				t.Fatal("rebuildLocked() error = nil, want member state error")
			}
			assertMDMemberWatchPreserved(
				t,
				watcher,
				previous,
				"md0",
				"sdb",
				"in_sync",
			)
		})
	}
}

func TestMDWatcherReportsArrayChangeDuringTopologyRebuild(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:      "raid1\n",
			mdSyncAction: "idle\n",
			mdDegraded:   "0\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	levelPath := filepath.Join(fixture.sysBlock, "md0", "md", mdLevel)
	syncPath := filepath.Join(fixture.sysBlock, "md0", "md", mdSyncAction)
	if err := os.WriteFile(levelPath, []byte("raid10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syncPath, []byte("reshape\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdLevel))
	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldSyncAction
	})
	if change.Array != "md0" || change.OldState != "idle" ||
		change.NewState != "reshape" {
		t.Fatalf("sync change = %+v", change)
	}

	writeMDDevice(t, fixture.sysBlock, "md1", map[string]string{
		mdLevel:      "raid1\n",
		mdSyncAction: "resync\n",
		mdDegraded:   "1\n",
	})
	writeMDStatFixture(t, fixture.mdstat, "md0", "md1")
	handleMDWatchEvent(
		t, watcher, mdFileTarget(t, watcher, "", "", mdStatField),
	)
	_ = mdFileTarget(t, watcher, "md1", "", mdSyncAction)
	assertNoMDChange(t, watcher)

	writeMDStatFixture(t, fixture.mdstat, "md1")
	handleMDWatchEvent(
		t, watcher, mdFileTarget(t, watcher, "", "", mdStatField),
	)
	assertNoMDChange(t, watcher)
}

func TestNormalizeMDSyncAction(t *testing.T) {
	for input, want := range map[string]string{
		"recover\n": "recover",
		"FROZEN":    "frozen",
		"reserved":  "unknown",
		"":          "unknown",
	} {
		if got := normalizeMDSyncAction(input); got != want {
			t.Fatalf("normalizeMDSyncAction(%q) = %q, want %q", input, got, want)
		}
	}
}
