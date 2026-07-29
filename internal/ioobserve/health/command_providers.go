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
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"huatuo-bamai/pkg/types"
)

const (
	evidenceCommandTimeout = 5 * time.Second
	evidenceOutputLimit    = 256 * 1024
)

type commandExecution struct {
	stdout   []byte
	stderr   []byte
	exitCode int
	tooLarge bool
	err      error
}

type commandRunner func(context.Context, string, []string, int) commandExecution

type boundedCommandOutput struct {
	mu        sync.Mutex
	remaining int
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	tooLarge  bool
}

type boundedStream struct {
	output *boundedCommandOutput
	stderr bool
}

func (w boundedStream) Write(p []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()

	keep := len(p)
	if keep > w.output.remaining {
		keep = w.output.remaining
		w.output.tooLarge = true
	}
	if keep > 0 {
		if w.stderr {
			_, _ = w.output.stderr.Write(p[:keep])
		} else {
			_, _ = w.output.stdout.Write(p[:keep])
		}
		w.output.remaining -= keep
	}
	return len(p), nil
}

func runEvidenceCommand(
	ctx context.Context,
	executable string,
	args []string,
	maxBytes int,
) commandExecution {
	output := &boundedCommandOutput{remaining: maxBytes}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout = boundedStream{output: output}
	cmd.Stderr = boundedStream{output: output, stderr: true}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return commandExecution{
		stdout:   output.stdout.Bytes(),
		stderr:   output.stderr.Bytes(),
		exitCode: exitCode,
		tooLarge: output.tooLarge,
		err:      err,
	}
}

func (w *EvidenceWorker) collectEvidence(
	ctx context.Context,
	request EvidenceRequest,
	target string,
) EvidenceResult {
	result := EvidenceResult{
		Target:      target,
		TriggeredAt: request.TriggeredAt,
		Event:       request.Trigger,
	}
	result.Event.NVMe = nil
	result.Event.SCSI = nil

	if request.Reason != "" {
		result.Reasons = []string{request.Reason}
		result.Event.CollectionStatus = "unsupported"
		return result
	}

	switch request.Protocol {
	case EvidenceProtocolNVMe:
		result.Event.NVMe, result.Reasons = w.collectNVMe(ctx, request.Target)
		result.Event.CollectionStatus = collectionStatus(
			result.Event.NVMe != nil,
			result.Reasons,
		)
	case EvidenceProtocolSCSI:
		result.Event.SCSI, result.Reasons = w.collectSCSI(ctx, request.Target)
		result.Event.CollectionStatus = collectionStatus(
			result.Event.SCSI != nil,
			result.Reasons,
		)
	default:
		result.Reasons = []string{CollectionReasonTargetUnsupported}
		result.Event.CollectionStatus = "unsupported"
	}
	return result
}

func (w *EvidenceWorker) collectNVMe(
	ctx context.Context,
	target string,
) (*types.NVMeHealthEvidence, []string) {
	executable, err := w.lookupPath("nvme")
	if err != nil || executable == "" {
		return nil, []string{CollectionReasonToolUnavailable}
	}

	devicePath := filepath.Join("/dev", target)
	evidence := &types.NVMeHealthEvidence{}
	trusted := false
	reasons := reasonSet{}

	smartResult := w.execute(ctx, executable, []string{
		"smart-log", "-o", "json", devicePath,
	})
	if reason := commandFailureReason(smartResult); reason != "" {
		reasons.add(reason)
	} else {
		smart, valid, complete := parseNVMeSMART(smartResult.stdout)
		if valid {
			evidence.CriticalWarning = smart.CriticalWarning
			evidence.MediaErrorsTotal = smart.MediaErrorsTotal
			trusted = true
		}
		if !valid || !complete {
			reasons.add(CollectionReasonParseError)
		}
	}

	errorResult := w.execute(ctx, executable, []string{
		"error-log", "-e", "16", "-o", "json", devicePath,
	})
	if reason := commandFailureReason(errorResult); reason != "" {
		reasons.add(reason)
	} else {
		errorLog, valid, complete := parseNVMeErrorLog(errorResult.stdout)
		if valid {
			evidence.ErrorLog = &errorLog
			trusted = true
		}
		if !valid || !complete {
			reasons.add(CollectionReasonParseError)
		}
	}

	if !trusted {
		return nil, reasons.values
	}
	return evidence, reasons.values
}

func (w *EvidenceWorker) collectSCSI(
	ctx context.Context,
	target string,
) (*types.SCSIHealthEvidence, []string) {
	executable, err := w.lookupPath("smartctl")
	if err != nil || executable == "" {
		return nil, []string{CollectionReasonToolUnavailable}
	}

	result := w.execute(ctx, executable, []string{
		"--health",
		"--attributes",
		"--log=error",
		"--json=c",
		filepath.Join("/dev", target),
	})
	if reason := commandFailureReasonBeforeExit(result); reason != "" {
		return nil, []string{reason}
	}

	evidence, trusted, complete, reportedExit := parseSCSIHealth(result.stdout)
	reasons := reasonSet{}
	if !trusted || !complete {
		reasons.add(CollectionReasonParseError)
	}

	exitStatus := result.exitCode
	if reportedExit != nil {
		exitStatus |= int(*reportedExit)
	}
	// smartctl bits 0..2 mean command line, device access, or command
	// execution failure. Bits 3..7 describe device health and do not make a
	// successfully parsed collection fail.
	if (result.err != nil && result.exitCode < 0) || exitStatus&0x07 != 0 {
		reasons.add(CollectionReasonExecError)
	}

	if !trusted {
		return nil, reasons.values
	}
	return evidence, reasons.values
}

func (w *EvidenceWorker) execute(
	parent context.Context,
	executable string,
	args []string,
) commandExecution {
	ctx, cancel := context.WithTimeout(parent, w.commandTimeout)
	defer cancel()

	result := w.runCommand(ctx, executable, args, w.maxOutputBytes)
	if ctx.Err() != nil {
		result.err = ctx.Err()
	}
	return result
}

func commandFailureReason(result commandExecution) string {
	if reason := commandFailureReasonBeforeExit(result); reason != "" {
		return reason
	}
	if result.err != nil || result.exitCode != 0 {
		return CollectionReasonExecError
	}
	return ""
}

func commandFailureReasonBeforeExit(result commandExecution) string {
	if result.tooLarge {
		return CollectionReasonOutputTooLarge
	}
	if errors.Is(result.err, context.DeadlineExceeded) {
		return CollectionReasonTimeout
	}
	if result.err != nil && result.exitCode < 0 {
		return CollectionReasonExecError
	}
	return ""
}

func collectionStatus(hasEvidence bool, reasons []string) string {
	if hasEvidence {
		if len(reasons) == 0 {
			return "ok"
		}
		return "partial"
	}
	for _, reason := range reasons {
		if reason == CollectionReasonTargetUnresolved ||
			reason == CollectionReasonToolUnavailable ||
			reason == CollectionReasonTargetUnsupported {
			return "unsupported"
		}
	}
	for _, reason := range reasons {
		if reason == CollectionReasonTimeout {
			return "timeout"
		}
	}
	return "error"
}

type reasonSet struct {
	seen   map[string]struct{}
	values []string
}

func (s *reasonSet) add(reason string) {
	if reason == "" {
		return
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if _, ok := s.seen[reason]; ok {
		return
	}
	s.seen[reason] = struct{}{}
	s.values = append(s.values, reason)
}
