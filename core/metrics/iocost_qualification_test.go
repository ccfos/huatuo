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

//go:build !didi

// The IOCOST qualification harness runs production objects on live kernels.
// Its unit tests cover configuration, decoding, and the runner protocol.
package collector

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"

	cebpf "github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
)

// Live kernel runtime harness.

// This file provides the IOCOST runtime harness used for live qualification.

const (
	ioCostRuntimeRequiredEnvironment = "TEST_IOCOST_REQUIRED"
	ioCostRuntimeObjectEnvironment   = "TEST_IOCOST_OBJECT"

	ioCostRuntimeDiagnosticFaultMaskEnvironment = "TEST_IOCOST_DIAG_FAULT_MASK"
	ioCostRuntimeDiagnosticMajorEnvironment     = "TEST_IOCOST_DIAG_MAJOR"
	ioCostRuntimeDiagnosticMinorEnvironment     = "TEST_IOCOST_DIAG_FIRST_MINOR"
	ioCostRuntimeDiagnosticCSSSerialEnvironment = "TEST_IOCOST_DIAG_CSS_SERIAL"
	ioCostRuntimeDiagnosticIOCGPtrEnvironment   = "TEST_IOCOST_DIAG_IOCG_PTR"
	ioCostRuntimeObserveMajorEnvironment        = "TEST_IOCOST_OBSERVE_MAJOR"
	ioCostRuntimeObserveMinorEnvironment        = "TEST_IOCOST_OBSERVE_FIRST_MINOR"

	ioCostRuntimeReadyFileEnvironment  = "TEST_IOCOST_READY_FILE"
	ioCostRuntimeDoneFileEnvironment   = "TEST_IOCOST_DONE_FILE"
	ioCostRuntimeResultFileEnvironment = "TEST_IOCOST_RESULT_FILE"
	ioCostRuntimeTimeoutEnvironment    = "TEST_IOCOST_TIMEOUT_SECONDS"
	ioCostRuntimeCheckpointEnvironment = "TEST_IOCOST_CHECKPOINT"

	ioCostRuntimeProductionObject    = "production"
	ioCostRuntimeDiagnosticObject    = "diagnostic"
	ioCostRuntimeObserveObject       = "observe"
	ioCostRuntimeQualificationObject = "qualification"

	ioCostRuntimeProductionObjectDirectory = "bpf"
	ioCostRuntimeDiagnosticObjectDirectory = "_output/test-bpf"

	ioCostRuntimeDefaultTimeout = 60 * time.Second
	ioCostRuntimeMaximumTimeout = 5 * time.Minute
	ioCostRuntimePollInterval   = 20 * time.Millisecond
	ioCostRuntimeStopTimeout    = 10 * time.Second
)

type ioCostRuntimeConfig struct {
	objectName        string
	objectDirectory   string
	diagnostic        bool
	diagnosticROData  map[string]any
	observe           bool
	qualification     bool
	checkpoint        bool
	observeMajor      uint32
	observeFirstMinor uint32
	readyFile         string
	doneFile          string
	resultFile        string
	timeout           time.Duration
}

type ioCostRuntimeStatusResult struct {
	Errno  int32  `json:"errno"`
	Reason uint32 `json:"reason"`
}

type ioCostRuntimeDiagnosticResult struct {
	StartGuardPasses        uint64 `json:"start_guard_passes"`
	WakeEntryHits           uint64 `json:"wake_entry_hits"`
	WakeReturnZero          uint64 `json:"wake_return_zero"`
	WakeReturnMinusOne      uint64 `json:"wake_return_minus_one"`
	SettledCount            uint64 `json:"settled_count"`
	MapFullInjections       uint64 `json:"map_full_injections"`
	CollisionInjections     uint64 `json:"collision_injections"`
	DeleteFailureInjections uint64 `json:"delete_failure_injections"`
}

type ioCostRuntimeObservationResult struct {
	Major         uint32            `json:"major"`
	FirstMinor    uint32            `json:"first_minor"`
	IOCPtr        string            `json:"ioc_ptr"`
	IOCID         string            `json:"ioc_id"`
	IOCGPtr       string            `json:"iocg_ptr"`
	CSS           string            `json:"css"`
	CSSSerial     string            `json:"css_serial"`
	IOCRows       uint32            `json:"ioc_rows"`
	OwnerRows     uint32            `json:"owner_rows"`
	AggregateRows uint32            `json:"aggregate_rows"`
	IOCs          map[string]string `json:"iocs,omitempty"`
}

type ioCostRuntimeDrainResult struct {
	PendingRows      uint32 `json:"pending_rows"`
	ActiveWakeFrames uint32 `json:"active_wake_frames"`
	Drained          bool   `json:"drained"`
}

type ioCostRuntimeMetricLabelResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ioCostRuntimeMetricResult struct {
	Name   string                           `json:"name"`
	Value  float64                          `json:"value"`
	Labels []ioCostRuntimeMetricLabelResult `json:"labels"`
}

type ioCostRuntimeBusinessIntervalsResult struct {
	First  []ioCostRuntimeMetricResult `json:"first"`
	Second []ioCostRuntimeMetricResult `json:"second"`
}

type ioCostRuntimeLaneObservationResult struct {
	CountLanes []int                          `json:"count_lanes"`
	WaitLanes  []int                          `json:"wait_lanes"`
	RawSeries  []ioCostRuntimeRawSeriesResult `json:"raw_series"`
}

type ioCostRuntimeRawSeriesResult struct {
	Device    string `json:"device"`
	Operation string `json:"operation"`
	IOCount   string `json:"io_count"`
	Wait10US  string `json:"wait_10us"`
}

type ioCostRuntimeResult struct {
	Status             ioCostRuntimeStatusResult             `json:"status"`
	DiagnosticCounters ioCostRuntimeDiagnosticResult         `json:"diagnostic_counters"`
	Observation        *ioCostRuntimeObservationResult       `json:"observation,omitempty"`
	Drain              *ioCostRuntimeDrainResult             `json:"drain,omitempty"`
	BusinessIntervals  *ioCostRuntimeBusinessIntervalsResult `json:"business_intervals,omitempty"`
	LaneObservation    *ioCostRuntimeLaneObservationResult   `json:"lane_observation,omitempty"`
	Error              string                                `json:"error"`
}

type ioCostRuntimeQualificationSteps struct {
	update          func() ([]*metric.Data, error)
	drain           func(context.Context) (ioCostRuntimeDrainResult, error)
	mapRows         func(context.Context) (ioCostRuntimeObservationResult, error)
	laneObservation func(context.Context) (ioCostRuntimeLaneObservationResult, error)
	status          func(context.Context) (ioCostStatus, error)
}

type ioCostRuntimeSession struct {
	tracing *iocostTracing
	session *ioCostSession
	cancel  context.CancelFunc
	done    <-chan error
}

// TestIOCostStartAttachSmoke is compiled into the default-backend OETest
// runtime binary. Without TEST_IOCOST_REQUIRED it remains inert during normal
// unit tests. Required mode is strict: no missing prerequisite is skipped.
func TestIOCostStartAttachSmoke(t *testing.T) {
	required, present := os.LookupEnv(ioCostRuntimeRequiredEnvironment)
	if !present || required == "" {
		t.Skip("live IOCOST runtime qualification was not requested")
	}
	if required != "1" {
		t.Fatalf("%s must be exactly 1, got %q",
			ioCostRuntimeRequiredEnvironment, required)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatalf("IOCOST runtime qualification requires linux/amd64, got %s/%s",
			runtime.GOOS, runtime.GOARCH)
	}
	if os.Geteuid() != 0 {
		t.Fatal("IOCOST runtime qualification requires root")
	}

	config, err := loadIOCostRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := runIOCostRuntime(config)
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if config.resultFile != "" {
		if err := writeIOCostRuntimeResult(config.resultFile, result); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
}

func loadIOCostRuntimeConfig() (ioCostRuntimeConfig, error) {
	config := ioCostRuntimeConfig{
		objectName:      ioCostObjectName,
		objectDirectory: ioCostRuntimeProductionObjectDirectory,
		timeout:         ioCostRuntimeDefaultTimeout,
	}

	if value := os.Getenv(ioCostRuntimeTimeoutEnvironment); value != "" {
		seconds, err := strconv.ParseUint(value, 10, 32)
		if err != nil || seconds == 0 {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s must be a positive integer: %q",
				ioCostRuntimeTimeoutEnvironment, value)
		}
		config.timeout = time.Duration(seconds) * time.Second
		if config.timeout > ioCostRuntimeMaximumTimeout {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s exceeds %s: %q",
				ioCostRuntimeTimeoutEnvironment,
				ioCostRuntimeMaximumTimeout,
				value,
			)
		}
	}

	mode := os.Getenv(ioCostRuntimeObjectEnvironment)
	if value, present := os.LookupEnv(ioCostRuntimeCheckpointEnvironment); present {
		if mode != ioCostRuntimeQualificationObject || value != "1" {
			return config, fmt.Errorf("%s requires qualification mode and value 1",
				ioCostRuntimeCheckpointEnvironment)
		}
		config.checkpoint = true
	}
	switch mode {
	case "", ioCostRuntimeProductionObject:
		if name := firstSetIOCostRuntimeInteractiveEnvironment(); name != "" {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s is only valid with %s=%s, %s=%s, or %s=%s",
				name,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeDiagnosticObject,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeObserveObject,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeQualificationObject,
			)
		}
		return config, nil
	case ioCostRuntimeDiagnosticObject:
		if name := firstSetIOCostRuntimeObserveEnvironment(); name != "" {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s is only valid with %s=%s",
				name,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeObserveObject,
			)
		}
		config.objectName = ioCostDiagnosticObjectName
		config.objectDirectory = ioCostRuntimeDiagnosticObjectDirectory
		config.diagnostic = true
		var err error
		config, err = loadIOCostRuntimeDiagnosticConfig(config)
		if err != nil {
			return ioCostRuntimeConfig{}, err
		}
		return loadIOCostRuntimeProtocolConfig(config)
	case ioCostRuntimeObserveObject:
		if name := firstSetIOCostRuntimeDiagnosticEnvironment(); name != "" {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s is only valid with %s=%s",
				name,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeDiagnosticObject,
			)
		}
		major, err := parseIOCostRuntimeUint(
			ioCostRuntimeObserveMajorEnvironment, 32, true)
		if err != nil {
			return ioCostRuntimeConfig{}, err
		}
		firstMinor, err := parseIOCostRuntimeUint(
			ioCostRuntimeObserveMinorEnvironment, 32, true)
		if err != nil {
			return ioCostRuntimeConfig{}, err
		}
		config.observe = true
		config.observeMajor = uint32(major)
		config.observeFirstMinor = uint32(firstMinor)
		return loadIOCostRuntimeProtocolConfig(config)
	case ioCostRuntimeQualificationObject:
		if name := firstSetIOCostRuntimeDiagnosticEnvironment(); name != "" {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s is not valid with %s=%s",
				name,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeQualificationObject,
			)
		}
		if name := firstSetIOCostRuntimeObserveEnvironment(); name != "" {
			return ioCostRuntimeConfig{}, fmt.Errorf(
				"%s is not valid with %s=%s",
				name,
				ioCostRuntimeObjectEnvironment,
				ioCostRuntimeQualificationObject,
			)
		}
		config.qualification = true
		return loadIOCostRuntimeProtocolConfig(config)
	default:
		return ioCostRuntimeConfig{}, fmt.Errorf(
			"%s must be %q, %q, %q, or %q, got %q",
			ioCostRuntimeObjectEnvironment,
			ioCostRuntimeProductionObject,
			ioCostRuntimeDiagnosticObject,
			ioCostRuntimeObserveObject,
			ioCostRuntimeQualificationObject,
			mode,
		)
	}
}

//nolint:gocritic // Return a validated config copy with newly owned diagnostic data.
func loadIOCostRuntimeDiagnosticConfig(
	config ioCostRuntimeConfig,
) (ioCostRuntimeConfig, error) {
	faultMask, err := parseIOCostRuntimeUint(
		ioCostRuntimeDiagnosticFaultMaskEnvironment, 32, true)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	if faultMask != 0 &&
		faultMask != uint64(ioCostDiagnosticFaultMapFull) &&
		faultMask != uint64(ioCostDiagnosticFaultCollision) &&
		faultMask != uint64(ioCostDiagnosticFaultDeleteFailure) {
		return ioCostRuntimeConfig{}, fmt.Errorf(
			"%s must be exactly 0, 1, 2, or 4, got %d",
			ioCostRuntimeDiagnosticFaultMaskEnvironment,
			faultMask,
		)
	}
	major, err := parseIOCostRuntimeUint(
		ioCostRuntimeDiagnosticMajorEnvironment, 32, true)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	firstMinor, err := parseIOCostRuntimeUint(
		ioCostRuntimeDiagnosticMinorEnvironment, 32, true)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	cssSerial, err := parseIOCostRuntimeUint(
		ioCostRuntimeDiagnosticCSSSerialEnvironment, 64, false)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	iocgPtr, err := parseIOCostRuntimeUint(
		ioCostRuntimeDiagnosticIOCGPtrEnvironment, 64, false)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	config.diagnosticROData = map[string]any{
		ioCostDiagnosticFaultMaskConstant:  uint32(faultMask),
		ioCostDiagnosticMajorConstant:      uint32(major),
		ioCostDiagnosticFirstMinorConstant: uint32(firstMinor),
		ioCostDiagnosticCSSSerialConstant:  cssSerial,
		ioCostDiagnosticIOCGPtrConstant:    iocgPtr,
	}
	return config, nil
}

//nolint:gocritic // Return a validated config copy after resolving protocol paths.
func loadIOCostRuntimeProtocolConfig(
	config ioCostRuntimeConfig,
) (ioCostRuntimeConfig, error) {
	var err error
	config.readyFile, err = requiredIOCostRuntimePath(
		ioCostRuntimeReadyFileEnvironment)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	config.doneFile, err = requiredIOCostRuntimePath(
		ioCostRuntimeDoneFileEnvironment)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	config.resultFile, err = requiredIOCostRuntimePath(
		ioCostRuntimeResultFileEnvironment)
	if err != nil {
		return ioCostRuntimeConfig{}, err
	}
	if err := validateIOCostRuntimeProtocolPaths(config); err != nil {
		return ioCostRuntimeConfig{}, err
	}
	return config, nil
}

func firstSetIOCostRuntimeDiagnosticEnvironment() string {
	for _, name := range []string{
		ioCostRuntimeDiagnosticFaultMaskEnvironment,
		ioCostRuntimeDiagnosticMajorEnvironment,
		ioCostRuntimeDiagnosticMinorEnvironment,
		ioCostRuntimeDiagnosticCSSSerialEnvironment,
		ioCostRuntimeDiagnosticIOCGPtrEnvironment,
	} {
		if _, present := os.LookupEnv(name); present {
			return name
		}
	}
	return ""
}

func firstSetIOCostRuntimeObserveEnvironment() string {
	for _, name := range []string{
		ioCostRuntimeObserveMajorEnvironment,
		ioCostRuntimeObserveMinorEnvironment,
	} {
		if _, present := os.LookupEnv(name); present {
			return name
		}
	}
	return ""
}

func firstSetIOCostRuntimeInteractiveEnvironment() string {
	for _, name := range []string{
		ioCostRuntimeDiagnosticFaultMaskEnvironment,
		ioCostRuntimeDiagnosticMajorEnvironment,
		ioCostRuntimeDiagnosticMinorEnvironment,
		ioCostRuntimeDiagnosticCSSSerialEnvironment,
		ioCostRuntimeDiagnosticIOCGPtrEnvironment,
		ioCostRuntimeObserveMajorEnvironment,
		ioCostRuntimeObserveMinorEnvironment,
		ioCostRuntimeReadyFileEnvironment,
		ioCostRuntimeDoneFileEnvironment,
		ioCostRuntimeResultFileEnvironment,
	} {
		if _, present := os.LookupEnv(name); present {
			return name
		}
	}
	return ""
}

func parseIOCostRuntimeUint(
	name string,
	bits int,
	allowZero bool,
) (uint64, error) {
	value, present := os.LookupEnv(name)
	if !present || value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) != value {
		return 0, fmt.Errorf("%s contains surrounding whitespace: %q", name, value)
	}
	parsed, err := strconv.ParseUint(value, 0, bits)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", name, value, err)
	}
	if !allowZero && parsed == 0 {
		return 0, fmt.Errorf("%s must be nonzero", name)
	}
	return parsed, nil
}

func requiredIOCostRuntimePath(name string) (string, error) {
	path, present := os.LookupEnv(name)
	if !present || path == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s=%q: %w", name, path, err)
	}
	return filepath.Clean(abs), nil
}

//nolint:gocritic // Validate one immutable runtime config snapshot.
func validateIOCostRuntimeProtocolPaths(config ioCostRuntimeConfig) error {
	paths := map[string]string{
		ioCostRuntimeReadyFileEnvironment:  config.readyFile,
		ioCostRuntimeDoneFileEnvironment:   config.doneFile,
		ioCostRuntimeResultFileEnvironment: config.resultFile,
	}
	if config.checkpoint {
		paths["checkpoint request"] = config.doneFile + ".checkpoint"
		paths["checkpoint result"] = config.resultFile + ".checkpoint"
	}
	seen := make(map[string]string, len(paths))
	for name, path := range paths {
		if previous, duplicate := seen[path]; duplicate {
			return fmt.Errorf("%s and %s resolve to the same path %q",
				previous, name, path)
		}
		seen[path] = name
		parent := filepath.Dir(path)
		info, err := os.Stat(parent)
		if err != nil {
			return fmt.Errorf("inspect parent directory for %s: %w", name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("parent for %s is not a directory: %s", name, parent)
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s already exists: %s", name, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
	}
	return nil
}

//nolint:gocritic // Run against one immutable parsed configuration snapshot.
func runIOCostRuntime(config ioCostRuntimeConfig) (
	result ioCostRuntimeResult,
	retErr error,
) {
	if err := bpf.Init(&bpf.Option{}); err != nil {
		return result, fmt.Errorf("initialize BPF runtime: %w", err)
	}
	defer bpf.Shutdown()

	profile, err := loadIOCostKernelProfile()
	if err != nil {
		return result, fmt.Errorf("resolve IOCOST kernel profile: %w", err)
	}
	possibleCPUs, err := cebpf.PossibleCPU()
	if err != nil {
		return result, fmt.Errorf("read possible CPU count: %w", err)
	}

	previousObjectDirectory := bpf.DefaultObjDir
	bpf.DefaultObjDir = config.objectDirectory
	defer func() {
		bpf.DefaultObjDir = previousObjectDirectory
	}()

	ctx, cancel := context.WithTimeout(context.Background(), config.timeout)
	loader := func(requestedObject string, constants map[string]any) (bpf.BPF, error) {
		if requestedObject != ioCostObjectName {
			return nil, fmt.Errorf("unexpected IOCOST object request %q", requestedObject)
		}
		rewrites := make(map[string]any, len(constants)+len(config.diagnosticROData))
		for name, value := range constants {
			rewrites[name] = value
		}
		for name, value := range config.diagnosticROData {
			rewrites[name] = value
		}
		return bpf.LoadBPF(config.objectName, rewrites)
	}
	emptyContainers := func() (map[string]*pod.Container, error) {
		return map[string]*pod.Container{}, nil
	}

	runtimeSession, err := startIOCostRuntimeSession(
		ctx,
		cancel,
		loader,
		profile,
		possibleCPUs,
		emptyContainers,
	)
	if err != nil {
		return result, err
	}
	defer func() {
		retErr = errors.Join(retErr, runtimeSession.stop())
	}()
	if config.qualification {
		return runIOCostRuntimeQualification(
			ctx,
			config,
			ioCostRuntimeQualificationSteps{
				update: runtimeSession.tracing.Update,
				drain: func(ctx context.Context) (
					ioCostRuntimeDrainResult,
					error,
				) {
					return pollIOCostRuntimeDrain(ctx, runtimeSession)
				},
				mapRows: func(ctx context.Context) (
					ioCostRuntimeObservationResult,
					error,
				) {
					return readIOCostRuntimeMapRows(ctx, runtimeSession)
				},
				laneObservation: func(ctx context.Context) (
					ioCostRuntimeLaneObservationResult,
					error,
				) {
					return captureIOCostRuntimeLaneObservation(
						ctx, runtimeSession)
				},
				status: func(ctx context.Context) (ioCostStatus, error) {
					return readIOCostRuntimeStatus(ctx, runtimeSession)
				},
			},
		)
	}

	if !config.diagnostic && !config.observe {
		status, err := readIOCostRuntimeStatus(ctx, runtimeSession)
		result.Status = makeIOCostRuntimeStatusResult(&status)
		if err != nil {
			return result, err
		}
		if status.Reason != 0 {
			return result, fmt.Errorf("production IOCOST object is unhealthy: %+v", status)
		}
		return result, nil
	}

	if err := writeIOCostRuntimeFile(config.readyFile, []byte("ready\n")); err != nil {
		return result, fmt.Errorf("publish IOCOST runtime ready file: %w", err)
	}
	if err := waitForIOCostRuntimeDone(ctx, config.doneFile); err != nil {
		return result, err
	}
	if config.observe {
		drain, err := pollIOCostRuntimeDrain(ctx, runtimeSession)
		result.Drain = &drain
		if err != nil {
			return result, err
		}
		observation, err := readIOCostRuntimeObservation(
			ctx,
			runtimeSession,
			config.observeMajor,
			config.observeFirstMinor,
		)
		result.Observation = &observation
		if err != nil {
			return result, err
		}
		status, err := readIOCostRuntimeStatus(ctx, runtimeSession)
		result.Status = makeIOCostRuntimeStatusResult(&status)
		if err != nil {
			return result, err
		}
		if status.Reason != 0 {
			return result, fmt.Errorf(
				"IOCOST runtime is unhealthy: %+v", status)
		}
		return result, nil
	}

	drain, err := pollIOCostRuntimeDrain(ctx, runtimeSession)
	result.Drain = &drain
	if err != nil {
		return result, err
	}
	diagnostics, err := readIOCostRuntimeDiagnostics(ctx, runtimeSession)
	result.DiagnosticCounters = makeIOCostRuntimeDiagnosticResult(diagnostics)
	if err != nil {
		return result, err
	}
	status, err := readIOCostRuntimeStatus(ctx, runtimeSession)
	result.Status = makeIOCostRuntimeStatusResult(&status)
	if err != nil {
		return result, err
	}
	return result, nil
}

//nolint:gocritic // Keep qualification inputs stable for the complete transaction.
func runIOCostRuntimeQualification(
	ctx context.Context,
	config ioCostRuntimeConfig,
	steps ioCostRuntimeQualificationSteps,
) (ioCostRuntimeResult, error) {
	result := ioCostRuntimeResult{
		BusinessIntervals: &ioCostRuntimeBusinessIntervalsResult{
			First:  make([]ioCostRuntimeMetricResult, 0),
			Second: make([]ioCostRuntimeMetricResult, 0),
		},
		LaneObservation: &ioCostRuntimeLaneObservationResult{
			CountLanes: make([]int, 0),
			WaitLanes:  make([]int, 0),
			RawSeries:  make([]ioCostRuntimeRawSeriesResult, 0),
		},
	}
	if steps.update == nil {
		return result, errors.New("IOCOST qualification Update is unavailable")
	}
	baseline, err := steps.update()
	if err != nil {
		return result, fmt.Errorf(
			"establish empty IOCOST qualification baseline: %w", err)
	}
	if len(baseline) != 0 {
		return result, fmt.Errorf(
			"IOCOST qualification baseline Update returned %d IOCOST metrics",
			len(baseline),
		)
	}
	if err := writeIOCostRuntimeFile(
		config.readyFile, []byte("ready\n")); err != nil {
		return result, fmt.Errorf("publish IOCOST runtime ready file: %w", err)
	}
	if config.checkpoint {
		// Observe the same session across teardown without consuming its
		// unpublished interval or introducing another business baseline.
		if steps.drain == nil || steps.mapRows == nil {
			return result, errors.New("IOCOST checkpoint readers are unavailable")
		}
		if err := waitForIOCostRuntimeDone(ctx, config.doneFile+".checkpoint"); err != nil {
			return result, err
		}
		drain, err := steps.drain(ctx)
		if err != nil {
			return result, err
		}
		observation, err := steps.mapRows(ctx)
		if err != nil {
			return result, err
		}
		if err := writeIOCostRuntimeResult(config.resultFile+".checkpoint",
			ioCostRuntimeResult{Drain: &drain, Observation: &observation}); err != nil {
			return result, err
		}
	}
	if err := waitForIOCostRuntimeDone(ctx, config.doneFile); err != nil {
		return result, err
	}
	if steps.drain == nil {
		return result, errors.New("IOCOST qualification drain is unavailable")
	}
	drain, err := steps.drain(ctx)
	result.Drain = &drain
	if err != nil {
		return result, err
	}
	if steps.mapRows == nil {
		return result, errors.New("IOCOST qualification map row reader is unavailable")
	}
	observation, err := steps.mapRows(ctx)
	result.Observation = &observation
	if err != nil {
		return result, fmt.Errorf("read IOCOST qualification map rows: %w", err)
	}
	if steps.laneObservation == nil {
		return result, errors.New(
			"IOCOST qualification lane observation is unavailable")
	}
	laneObservation, err := steps.laneObservation(ctx)
	result.LaneObservation = &laneObservation
	if err != nil {
		return result, fmt.Errorf(
			"capture IOCOST qualification lane observation: %w", err)
	}

	first, err := steps.update()
	if err != nil {
		return result, fmt.Errorf("first IOCOST business Update: %w", err)
	}
	second, secondUpdateErr := steps.update()
	firstResult, firstEncodeErr := makeIOCostRuntimeMetricResults(first)
	result.BusinessIntervals.First = firstResult
	var intervalErr error
	if firstEncodeErr != nil {
		intervalErr = fmt.Errorf(
			"encode first IOCOST business interval: %w", firstEncodeErr)
	}
	if secondUpdateErr != nil {
		return result, errors.Join(intervalErr, fmt.Errorf(
			"second IOCOST business Update: %w", secondUpdateErr))
	}
	secondResult, secondEncodeErr := makeIOCostRuntimeMetricResults(second)
	result.BusinessIntervals.Second = secondResult
	if secondEncodeErr != nil {
		intervalErr = errors.Join(intervalErr, fmt.Errorf(
			"encode second IOCOST business interval: %w", secondEncodeErr))
	}
	if intervalErr != nil {
		return result, intervalErr
	}

	if steps.status == nil {
		return result, errors.New("IOCOST qualification status is unavailable")
	}
	status, err := steps.status(ctx)
	result.Status = makeIOCostRuntimeStatusResult(&status)
	if err != nil {
		return result, err
	}
	if status.Reason != 0 {
		return result, fmt.Errorf(
			"IOCOST qualification runtime is unhealthy: %+v", status)
	}
	return result, nil
}

func makeIOCostRuntimeMetricResults(
	data []*metric.Data,
) ([]ioCostRuntimeMetricResult, error) {
	results := make([]ioCostRuntimeMetricResult, 0, len(data))
	for index, point := range data {
		result, err := makeIOCostRuntimeMetricResult(point)
		if err != nil {
			return nil, fmt.Errorf("IOCOST metric %d: %w", index, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func makeIOCostRuntimeMetricResult(
	data *metric.Data,
) (ioCostRuntimeMetricResult, error) {
	var result ioCostRuntimeMetricResult
	if data == nil {
		return result, errors.New("is nil")
	}
	value := reflect.ValueOf(data)
	if value.Kind() != reflect.Pointer || value.IsNil() ||
		value.Elem().Kind() != reflect.Struct {
		return result, errors.New("has invalid concrete representation")
	}
	value = value.Elem()
	name := value.FieldByName("name")
	valueType := value.FieldByName("valueType")
	labelKeys := value.FieldByName("labelKey")
	labelValues := value.FieldByName("labelValue")
	if !name.IsValid() || name.Kind() != reflect.String ||
		!valueType.IsValid() || valueType.Kind() != reflect.Int ||
		!labelKeys.IsValid() || labelKeys.Kind() != reflect.Slice ||
		!labelValues.IsValid() || labelValues.Kind() != reflect.Slice {
		return result, errors.New("does not match metric.Data ABI")
	}
	result.Name = name.String()
	if !isIOCostRuntimeMetricName(result.Name) {
		return result, fmt.Errorf(
			"unexpected IOCOST metric name %q", result.Name)
	}
	if int(valueType.Int()) != metric.MetricTypeGauge {
		return result, fmt.Errorf(
			"IOCOST metric %q is not a gauge", result.Name)
	}
	if math.IsNaN(data.Value) || math.IsInf(data.Value, 0) {
		return result, fmt.Errorf(
			"IOCOST metric %q has non-finite value %v",
			result.Name,
			data.Value,
		)
	}
	result.Value = data.Value
	if labelKeys.Len() != labelValues.Len() {
		return result, fmt.Errorf(
			"IOCOST metric %q has %d label keys and %d values",
			result.Name,
			labelKeys.Len(),
			labelValues.Len(),
		)
	}
	result.Labels = make([]ioCostRuntimeMetricLabelResult, labelKeys.Len())
	seenLabels := make(map[string]struct{}, labelKeys.Len())
	for index := 0; index < labelKeys.Len(); index++ {
		key := labelKeys.Index(index)
		value := labelValues.Index(index)
		if key.Kind() != reflect.String || value.Kind() != reflect.String {
			return ioCostRuntimeMetricResult{}, fmt.Errorf(
				"IOCOST metric %q label %d is not a string pair",
				result.Name,
				index,
			)
		}
		name := key.String()
		if name == "" {
			return ioCostRuntimeMetricResult{}, fmt.Errorf(
				"IOCOST metric %q label %d has an empty name",
				result.Name,
				index,
			)
		}
		if _, duplicate := seenLabels[name]; duplicate {
			return ioCostRuntimeMetricResult{}, fmt.Errorf(
				"IOCOST metric %q has duplicate label %q",
				result.Name,
				name,
			)
		}
		seenLabels[name] = struct{}{}
		result.Labels[index] = ioCostRuntimeMetricLabelResult{
			Name:  name,
			Value: value.String(),
		}
	}
	return result, nil
}

func isIOCostRuntimeMetricName(name string) bool {
	switch name {
	case ioCostWaitCountName,
		ioCostAverageWaitName,
		"container_" + ioCostWaitCountName,
		"container_" + ioCostAverageWaitName:
		return true
	default:
		return false
	}
}

func startIOCostRuntimeSession(
	ctx context.Context,
	cancel context.CancelFunc,
	loader ioCostBPFLoader,
	profile *ioCostKernelProfile,
	possibleCPUs int,
	containerSource ioControlContainerSource,
) (*ioCostRuntimeSession, error) {
	tracing := &iocostTracing{}
	done := make(chan error, 1)
	go func() {
		done <- tracing.startWithProfile(
			ctx,
			loader,
			profile,
			possibleCPUs,
			containerSource,
		)
	}()

	ticker := time.NewTicker(ioCostRuntimePollInterval)
	defer ticker.Stop()
	for {
		tracing.mu.Lock()
		session := tracing.session
		tracing.mu.Unlock()
		if session != nil {
			return &ioCostRuntimeSession{
				tracing: tracing,
				session: session,
				cancel:  cancel,
				done:    done,
			}, nil
		}

		select {
		case err := <-done:
			cancel()
			if err == nil {
				err = errors.New("IOCOST session ended before publication")
			}
			return nil, err
		case <-ctx.Done():
			cancel()
			stopErr := waitForIOCostRuntimeStop(done)
			return nil, errors.Join(
				fmt.Errorf("wait for IOCOST session publication: %w", ctx.Err()),
				stopErr,
			)
		case <-ticker.C:
		}
	}
}

func (session *ioCostRuntimeSession) stop() error {
	if session == nil {
		return nil
	}
	session.cancel()
	return waitForIOCostRuntimeStop(session.done)
}

func waitForIOCostRuntimeStop(done <-chan error) error {
	timer := time.NewTimer(ioCostRuntimeStopTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("stop IOCOST runtime session: %w", err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("stop IOCOST runtime session: timed out after %s",
			ioCostRuntimeStopTimeout)
	}
}

func waitForIOCostRuntimeDone(ctx context.Context, path string) error {
	ticker := time.NewTicker(ioCostRuntimePollInterval)
	defer ticker.Stop()
	for {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			return nil
		case err == nil:
			return fmt.Errorf("IOCOST runtime done path is not a regular file: %s", path)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("inspect IOCOST runtime done file: %w", err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for IOCOST runtime done file %s: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}

func readIOCostRuntimeStatus(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostStatus, error) {
	if err := ctx.Err(); err != nil {
		return ioCostStatus{}, err
	}
	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	if runtimeSession.tracing.session == nil {
		return ioCostStatus{}, errors.New("IOCOST runtime session was withdrawn")
	}
	status, err := runtimeSession.session.readStatus()
	if err != nil {
		return ioCostStatus{}, fmt.Errorf("read IOCOST runtime status: %w", err)
	}
	return status, nil
}

func captureIOCostRuntimeLaneObservation(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostRuntimeLaneObservationResult, error) {
	observation := newIOCostRuntimeLaneObservationResult()
	if ctx == nil {
		return observation, errors.New("nil IOCOST lane observation context")
	}
	if runtimeSession == nil || runtimeSession.tracing == nil {
		return observation, errors.New(
			"IOCOST lane observation runtime is unavailable")
	}

	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	session := runtimeSession.session
	if runtimeSession.tracing.session != session {
		return observation, errors.New(
			"IOCOST lane observation session was withdrawn")
	}
	if session == nil {
		return observation, errors.New(
			"IOCOST lane observation session is unavailable")
	}
	if err := session.checkBreaker(); err != nil {
		return observation, err
	}

	snapshot, err := session.captureRawSnapshot()
	if err != nil {
		return observation, fmt.Errorf(
			"capture IOCOST lane raw snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	if err := session.checkBreaker(); err != nil {
		return observation, err
	}
	return makeIOCostRuntimeLaneObservation(
		snapshot, session.possibleCPUs)
}

func newIOCostRuntimeLaneObservationResult() ioCostRuntimeLaneObservationResult {
	return ioCostRuntimeLaneObservationResult{
		CountLanes: make([]int, 0),
		WaitLanes:  make([]int, 0),
		RawSeries:  make([]ioCostRuntimeRawSeriesResult, 0),
	}
}

type ioCostRuntimeRawSeriesKey struct {
	device    string
	operation string
}

func makeIOCostRuntimeLaneObservation(
	snapshot *ioCostRawSnapshot,
	possibleCPUs int,
) (ioCostRuntimeLaneObservationResult, error) {
	observation := newIOCostRuntimeLaneObservationResult()
	if snapshot == nil {
		return observation, errors.New("IOCOST lane raw snapshot is nil")
	}
	if possibleCPUs <= 0 {
		return observation, fmt.Errorf(
			"invalid IOCOST lane possible CPU count: %d", possibleCPUs)
	}

	countLanes := make(map[int]struct{})
	waitLanes := make(map[int]struct{})
	rawSeries := make(map[ioCostRuntimeRawSeriesKey]ioCostCumulative)
	for key, sample := range snapshot.Samples {
		if len(sample.Counters) != possibleCPUs {
			return observation, fmt.Errorf(
				"IOCOST lane raw sample %+v has %d CPU lanes, want %d",
				key,
				len(sample.Counters),
				possibleCPUs,
			)
		}
		if sample.Device == "" {
			return observation, fmt.Errorf(
				"IOCOST lane raw sample %+v has an empty device", key)
		}
		if sample.Operation != "read" && sample.Operation != "write" {
			return observation, fmt.Errorf(
				"IOCOST lane raw sample %+v has invalid operation %q",
				key,
				sample.Operation,
			)
		}

		var sampleTotal ioCostCumulative
		for cpu, counters := range sample.Counters {
			if counters.IOCount != 0 {
				countLanes[cpu] = struct{}{}
			}
			if counters.Wait10US != 0 {
				waitLanes[cpu] = struct{}{}
			}
			sampleTotal.IOCount += counters.IOCount
			sampleTotal.Wait10US += counters.Wait10US
		}
		if sampleTotal.IOCount == 0 && sampleTotal.Wait10US != 0 {
			return observation, fmt.Errorf(
				"IOCOST lane raw sample %+v has wait_10us %d with no IO",
				key,
				sampleTotal.Wait10US,
			)
		}

		seriesKey := ioCostRuntimeRawSeriesKey{
			device:    sample.Device,
			operation: sample.Operation,
		}
		seriesTotal := rawSeries[seriesKey]
		seriesTotal.IOCount += sampleTotal.IOCount
		seriesTotal.Wait10US += sampleTotal.Wait10US
		rawSeries[seriesKey] = seriesTotal
	}

	for cpu := range countLanes {
		observation.CountLanes = append(observation.CountLanes, cpu)
	}
	for cpu := range waitLanes {
		observation.WaitLanes = append(observation.WaitLanes, cpu)
	}
	sort.Ints(observation.CountLanes)
	sort.Ints(observation.WaitLanes)

	seriesKeys := make([]ioCostRuntimeRawSeriesKey, 0, len(rawSeries))
	for key := range rawSeries {
		seriesKeys = append(seriesKeys, key)
	}
	sort.Slice(seriesKeys, func(left, right int) bool {
		if seriesKeys[left].device != seriesKeys[right].device {
			return seriesKeys[left].device < seriesKeys[right].device
		}
		return seriesKeys[left].operation < seriesKeys[right].operation
	})
	for _, key := range seriesKeys {
		counters := rawSeries[key]
		observation.RawSeries = append(
			observation.RawSeries,
			ioCostRuntimeRawSeriesResult{
				Device:    key.device,
				Operation: key.operation,
				IOCount:   strconv.FormatUint(counters.IOCount, 10),
				Wait10US:  strconv.FormatUint(counters.Wait10US, 10),
			},
		)
	}
	return observation, nil
}

func readIOCostRuntimeDiagnostics(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostDiagnosticStatus, error) {
	if err := ctx.Err(); err != nil {
		return ioCostDiagnosticStatus{}, err
	}
	if runtimeSession == nil || runtimeSession.tracing == nil {
		return ioCostDiagnosticStatus{}, errors.New(
			"IOCOST diagnostic runtime is unavailable")
	}
	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	session := runtimeSession.session
	if runtimeSession.tracing.session != session {
		return ioCostDiagnosticStatus{}, errors.New(
			"IOCOST diagnostic session was withdrawn")
	}
	if session == nil || session.object == nil {
		return ioCostDiagnosticStatus{}, errors.New(
			"IOCOST diagnostic session is unavailable")
	}
	mapID := session.object.MapIDByName(ioCostDiagnosticStatusMap)
	if mapID == 0 {
		return ioCostDiagnosticStatus{}, fmt.Errorf(
			"IOCOST diagnostic map %s is unavailable",
			ioCostDiagnosticStatusMap)
	}
	value, err := session.object.ReadMap(mapID, make([]byte, ioCostUint32Size))
	if err != nil {
		return ioCostDiagnosticStatus{}, fmt.Errorf(
			"read IOCOST diagnostic map %s: %w",
			ioCostDiagnosticStatusMap, err)
	}
	diagnostics, err := decodeIOCostDiagnosticStatus(value)
	if err != nil {
		return ioCostDiagnosticStatus{}, fmt.Errorf(
			"decode IOCOST diagnostic map %s: %w",
			ioCostDiagnosticStatusMap, err)
	}
	return diagnostics, nil
}

func readIOCostRuntimeMapRows(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostRuntimeObservationResult, error) {
	var observation ioCostRuntimeObservationResult
	if ctx == nil {
		return observation, errors.New("nil IOCOST map row context")
	}
	if runtimeSession == nil || runtimeSession.tracing == nil {
		return observation, errors.New("IOCOST map row runtime is unavailable")
	}
	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	session := runtimeSession.session
	if runtimeSession.tracing.session != session {
		return observation, errors.New("IOCOST map row session was withdrawn")
	}
	if session == nil {
		return observation, errors.New("IOCOST map row session is unavailable")
	}
	if err := session.checkBreaker(); err != nil {
		return observation, err
	}

	// Count the dumps before the public join discards orphan rows.
	for _, row := range []struct {
		name  string
		count *uint32
	}{
		{ioCostIOCStateMap, &observation.IOCRows},
		{ioCostOwnerStateMap, &observation.OwnerRows},
		{ioCostWaitAggregateMap, &observation.AggregateRows},
	} {
		items, err := session.dumpRequiredMap(row.name)
		if err != nil {
			return observation, fmt.Errorf("dump IOCOST map rows %s: %w", row.name, err)
		}
		*row.count = uint32(len(items))
		if row.name == ioCostIOCStateMap {
			indexes, err := decodeIOCostIOCIndexes(items)
			if err != nil {
				return observation, err
			}
			observation.IOCs = make(map[string]string, len(items))
			for id, device := range indexes.live {
				observation.IOCs[formatIOCostRuntimeUint64(id)] = device
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	return observation, session.checkBreaker()
}

func readIOCostRuntimeObservation(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
	major uint32,
	firstMinor uint32,
) (ioCostRuntimeObservationResult, error) {
	var observation ioCostRuntimeObservationResult
	if ctx == nil {
		return observation, errors.New("nil IOCOST observation context")
	}
	if runtimeSession == nil || runtimeSession.tracing == nil {
		return observation,
			errors.New("IOCOST observation runtime is unavailable")
	}
	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	session := runtimeSession.session
	if runtimeSession.tracing.session != session {
		return observation,
			errors.New("IOCOST observation session was withdrawn")
	}
	if session == nil {
		return observation,
			errors.New("IOCOST observation session is unavailable")
	}
	if err := session.checkBreaker(); err != nil {
		return observation, err
	}

	aggregateItems, err := session.dumpRequiredMap(ioCostWaitAggregateMap)
	if err != nil {
		return observation, fmt.Errorf(
			"dump IOCOST observation %s: %w", ioCostWaitAggregateMap, err)
	}
	aggregates, err := decodeIOCostAggregateSnapshot(
		aggregateItems, session.possibleCPUs)
	if err != nil {
		return observation,
			fmt.Errorf("decode IOCOST observation aggregates: %w", err)
	}
	ownerItems, err := session.dumpRequiredMap(ioCostOwnerStateMap)
	if err != nil {
		return observation,
			fmt.Errorf("dump IOCOST observation %s: %w", ioCostOwnerStateMap, err)
	}
	iocItems, err := session.dumpRequiredMap(ioCostIOCStateMap)
	if err != nil {
		return observation,
			fmt.Errorf("dump IOCOST observation %s: %w", ioCostIOCStateMap, err)
	}
	observation, err = decodeIOCostRuntimeObservation(
		ctx,
		major,
		firstMinor,
		iocItems,
		ownerItems,
		aggregates,
	)
	if err != nil {
		return observation, err
	}
	if err := session.checkBreaker(); err != nil {
		return observation, err
	}
	return observation, nil
}

func pollIOCostRuntimeDrain(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostRuntimeDrainResult, error) {
	return pollIOCostRuntimeDrainWithInterval(
		ctx, runtimeSession, ioCostRuntimePollInterval)
}

func pollIOCostRuntimeDrainWithInterval(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
	interval time.Duration,
) (ioCostRuntimeDrainResult, error) {
	var drain ioCostRuntimeDrainResult
	if ctx == nil {
		return drain, errors.New("nil IOCOST drain context")
	}
	if interval <= 0 {
		return drain, fmt.Errorf(
			"invalid IOCOST drain poll interval: %s", interval)
	}
	for {
		candidate, retry, err := readIOCostRuntimeDrainOnce(
			ctx, runtimeSession)
		drain = candidate
		if err != nil {
			return drain, err
		}
		if !retry {
			return drain, nil
		}
		if err := waitForIOCostRuntimePoll(
			ctx, interval, "pending requests and wake frames to drain"); err != nil {
			return drain, err
		}
	}
}

func readIOCostRuntimeDrainOnce(
	ctx context.Context,
	runtimeSession *ioCostRuntimeSession,
) (ioCostRuntimeDrainResult, bool, error) {
	var drain ioCostRuntimeDrainResult
	if runtimeSession == nil || runtimeSession.tracing == nil {
		return drain, false, errors.New("IOCOST drain runtime is unavailable")
	}
	runtimeSession.tracing.mu.Lock()
	defer runtimeSession.tracing.mu.Unlock()
	session := runtimeSession.session
	if runtimeSession.tracing.session != session {
		return drain, false, errors.New("IOCOST drain session was withdrawn")
	}
	if session == nil {
		return drain, false, errors.New("IOCOST drain session is unavailable")
	}
	if err := session.checkBreaker(); err != nil {
		return drain, false, err
	}
	pendingItems, err := session.dumpRequiredMap(ioCostPendingMap)
	if err != nil {
		return drain, false,
			fmt.Errorf("dump IOCOST drain %s: %w", ioCostPendingMap, err)
	}
	wakeItems, err := session.dumpRequiredMap(ioCostWakeFrameMap)
	if err != nil {
		return drain, false,
			fmt.Errorf("dump IOCOST drain %s: %w", ioCostWakeFrameMap, err)
	}
	drain, err = decodeIOCostRuntimeDrain(
		ctx, session.possibleCPUs, pendingItems, wakeItems)
	if err != nil {
		return drain, false, err
	}
	if err := session.checkBreaker(); err != nil {
		return drain, false, err
	}
	return drain, !drain.Drained, nil
}

func waitForIOCostRuntimePoll(
	ctx context.Context,
	interval time.Duration,
	description string,
) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf(
			"wait for IOCOST runtime %s: %w", description, ctx.Err())
	case <-timer.C:
		return nil
	}
}

type ioCostRuntimeObservedOwner struct {
	pointer uint64
	state   ioCostOwnerState
}

func decodeIOCostRuntimeObservation(
	ctx context.Context,
	major uint32,
	firstMinor uint32,
	iocItems []bpf.MapItem,
	ownerItems []bpf.MapItem,
	aggregates map[ioCostWaitKey]ioCostAggregateSample,
) (ioCostRuntimeObservationResult, error) {
	observation := ioCostRuntimeObservationResult{
		Major:         major,
		FirstMinor:    firstMinor,
		IOCRows:       uint32(len(iocItems)),
		OwnerRows:     uint32(len(ownerItems)),
		AggregateRows: uint32(len(aggregates)),
	}
	iocs, err := decodeIOCostIOCIndexes(iocItems)
	if err != nil {
		return observation, fmt.Errorf("decode IOCOST observation IOCs: %w", err)
	}
	var targetIOCPtr uint64
	var targetIOC ioCostIOCState
	for pointer, state := range iocs.byPointer {
		if state.Device != uint64(major)<<32|uint64(firstMinor) {
			continue
		}
		if targetIOCPtr != 0 {
			return observation, fmt.Errorf(
				"multiple IOCs for target device %d:%d", major, firstMinor)
		}
		targetIOCPtr = pointer
		targetIOC = state
	}
	if targetIOCPtr == 0 {
		return observation,
			fmt.Errorf("no IOC for target device %d:%d", major, firstMinor)
	}

	owners, err := decodeIOCostRuntimeObservedOwners(ctx, ownerItems, iocs.byPointer)
	if err != nil {
		return observation, err
	}
	var targetOwner ioCostRuntimeObservedOwner
	for _, owner := range owners {
		if owner.state.IOCPtr != targetIOCPtr ||
			owner.state.IOCID != targetIOC.IOCID {
			continue
		}
		if targetOwner.pointer != 0 {
			return observation, fmt.Errorf(
				"multiple owners for target device %d:%d", major, firstMinor)
		}
		targetOwner = owner
	}
	if targetOwner.pointer == 0 {
		return observation,
			fmt.Errorf("no owner for target device %d:%d", major, firstMinor)
	}
	observation.IOCPtr = formatIOCostRuntimeUint64(targetIOCPtr)
	observation.IOCID = formatIOCostRuntimeUint64(targetIOC.IOCID)
	observation.IOCGPtr = formatIOCostRuntimeUint64(targetOwner.pointer)
	observation.CSS = formatIOCostRuntimeUint64(targetOwner.state.CSS)
	observation.CSSSerial = formatIOCostRuntimeUint64(
		targetOwner.state.CSSSerial)
	return observation, nil
}

func decodeIOCostRuntimeObservedOwners(
	ctx context.Context,
	items []bpf.MapItem,
	iocs map[uint64]ioCostIOCState,
) ([]ioCostRuntimeObservedOwner, error) {
	owners := make([]ioCostRuntimeObservedOwner, 0, len(items))
	seenIdentities := make(map[ioCostOwnerIdentity]struct{}, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pointer, err := decodeIOCostUint64(item.Key)
		if err != nil {
			return nil, fmt.Errorf("decode IOCOST observation owner key: %w", err)
		}
		if pointer == 0 {
			return nil, errors.New("zero IOCOST observation owner pointer")
		}
		owner, err := decodeIOCostOwnerState(item.Value)
		if err != nil {
			return nil, fmt.Errorf(
				"decode IOCOST observation owner value for %#x: %w", pointer, err)
		}
		if owner.IOCPtr == 0 || owner.IOCID == 0 || owner.CSS == 0 ||
			owner.CSSSerial == 0 {
			return nil, fmt.Errorf(
				"zero IOCOST observation owner identity for %#x: %+v",
				pointer,
				owner,
			)
		}
		identity := ioCostOwnerIdentity{
			IOCID: owner.IOCID, CSSSerial: owner.CSSSerial,
		}
		if _, exists := seenIdentities[identity]; exists {
			return nil, fmt.Errorf(
				"duplicate IOCOST observation owner identity: %+v", identity)
		}
		seenIdentities[identity] = struct{}{}
		parent, exists := iocs[owner.IOCPtr]
		if !exists {
			// ioc_rqos_exit may remove an unrelated IOC while its owners are
			// still visible. Match the production snapshot teardown rule.
			continue
		}
		if parent.IOCID != owner.IOCID {
			return nil, fmt.Errorf(
				"IOCOST observation owner %#x IOC ID %d does not match parent %d",
				pointer,
				owner.IOCID,
				parent.IOCID,
			)
		}
		owners = append(owners, ioCostRuntimeObservedOwner{
			pointer: pointer, state: owner,
		})
	}
	return owners, nil
}

func validateIOCostRuntimePendingItems(
	ctx context.Context,
	items []bpf.MapItem,
) error {
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		pointer, err := decodeIOCostUint64(item.Key)
		if err != nil {
			return fmt.Errorf("decode IOCOST observation pending key: %w", err)
		}
		if pointer == 0 {
			return errors.New("zero IOCOST observation pending pointer")
		}
		pending, err := decodeIOCostPending(item.Value)
		if err != nil {
			return fmt.Errorf(
				"decode IOCOST observation pending value for %#x: %w", pointer, err)
		}
		if pending.StartNS == 0 {
			return fmt.Errorf(
				"zero IOCOST observation pending start time for %#x",
				pointer,
			)
		}
		if _, ok := ioOperationName(pending.Operation); !ok {
			return fmt.Errorf(
				"unknown IOCOST observation pending operation %d for %#x",
				pending.Operation,
				pointer,
			)
		}
	}
	return nil
}

func decodeIOCostRuntimeDrain(
	ctx context.Context,
	possibleCPUs int,
	pendingItems []bpf.MapItem,
	wakeItems []bpf.MapItem,
) (ioCostRuntimeDrainResult, error) {
	drain := ioCostRuntimeDrainResult{
		PendingRows: uint32(len(pendingItems)),
	}
	if err := validateIOCostRuntimePendingItems(ctx, pendingItems); err != nil {
		return drain, err
	}
	activeWakeFrames, err := decodeIOCostRuntimeActiveWakeFrames(
		wakeItems, possibleCPUs)
	if err != nil {
		return drain, err
	}
	drain.ActiveWakeFrames = activeWakeFrames
	drain.Drained = drain.PendingRows == 0 && drain.ActiveWakeFrames == 0
	return drain, nil
}

func decodeIOCostRuntimeActiveWakeFrames(
	items []bpf.MapItem,
	possibleCPUs int,
) (uint32, error) {
	if len(items) != 1 {
		return 0, fmt.Errorf(
			"IOCOST observation wake frame map returned %d rows, want 1",
			len(items),
		)
	}
	if err := requireIOCostDataSize(items[0].Key, ioCostUint32Size); err != nil {
		return 0, fmt.Errorf("decode IOCOST observation wake frame key: %w", err)
	}
	if binary.LittleEndian.Uint32(items[0].Key) != 0 {
		return 0, fmt.Errorf(
			"IOCOST observation wake frame key is not zero: %v", items[0].Key)
	}
	frames, err := decodeIOCostWakeFrames(items[0].Value, possibleCPUs)
	if err != nil {
		return 0, fmt.Errorf("decode IOCOST observation wake frames: %w", err)
	}
	var active uint32
	for _, frame := range frames {
		if frame.BioPtr != 0 {
			active++
		}
	}
	return active, nil
}

func formatIOCostRuntimeUint64(value uint64) string {
	return fmt.Sprintf("0x%x", value)
}

func makeIOCostRuntimeStatusResult(status *ioCostStatus) ioCostRuntimeStatusResult {
	return ioCostRuntimeStatusResult(*status)
}

//nolint:gocritic // Convert one stable diagnostic-counter snapshot.
func makeIOCostRuntimeDiagnosticResult(
	diagnostics ioCostDiagnosticStatus,
) ioCostRuntimeDiagnosticResult {
	return ioCostRuntimeDiagnosticResult(diagnostics)
}

//nolint:gocritic // Serialize one immutable qualification result snapshot.
func writeIOCostRuntimeResult(path string, result ioCostRuntimeResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode IOCOST runtime result: %w", err)
	}
	data = append(data, '\n')
	if err := writeIOCostRuntimeFile(path, data); err != nil {
		return fmt.Errorf("write IOCOST runtime result: %w", err)
	}
	return nil
}

func writeIOCostRuntimeFile(path string, data []byte) (retErr error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(
		directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if err := temporary.Close(); err != nil && retErr == nil {
				retErr = err
			}
		}
		if err := os.Remove(temporaryPath); err != nil &&
			!errors.Is(err, os.ErrNotExist) && retErr == nil {
			retErr = err
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	closeErr := temporary.Close()
	closed = true
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func TestIOCostRuntimeObjectModes(t *testing.T) {
	t.Run("production defaults remain inert", func(t *testing.T) {
		unsetIOCostRuntimeTestEnvironment(t)

		config, err := loadIOCostRuntimeConfig()
		require.NoError(t, err)
		require.Equal(t, ioCostObjectName, config.objectName)
		require.Equal(t, ioCostRuntimeProductionObjectDirectory,
			config.objectDirectory)
		require.False(t, config.diagnostic)
		require.False(t, config.observe)
		require.False(t, config.qualification)
		require.Empty(t, config.readyFile)
		require.Empty(t, config.doneFile)
		require.Empty(t, config.resultFile)
	})

	t.Run("diagnostic contract remains unchanged", func(t *testing.T) {
		unsetIOCostRuntimeTestEnvironment(t)
		t.Setenv(ioCostRuntimeObjectEnvironment,
			ioCostRuntimeDiagnosticObject)
		t.Setenv(ioCostRuntimeDiagnosticFaultMaskEnvironment, "2")
		t.Setenv(ioCostRuntimeDiagnosticMajorEnvironment, "8")
		t.Setenv(ioCostRuntimeDiagnosticMinorEnvironment, "16")
		t.Setenv(ioCostRuntimeDiagnosticCSSSerialEnvironment, "0x1234")
		t.Setenv(ioCostRuntimeDiagnosticIOCGPtrEnvironment, "0x5678")
		setIOCostRuntimeProtocolEnvironment(t)

		config, err := loadIOCostRuntimeConfig()
		require.NoError(t, err)
		require.True(t, config.diagnostic)
		require.False(t, config.observe)
		require.False(t, config.qualification)
		require.Equal(t, ioCostDiagnosticObjectName, config.objectName)
		require.Equal(t, map[string]any{
			ioCostDiagnosticFaultMaskConstant:  uint32(2),
			ioCostDiagnosticMajorConstant:      uint32(8),
			ioCostDiagnosticFirstMinorConstant: uint32(16),
			ioCostDiagnosticCSSSerialConstant:  uint64(0x1234),
			ioCostDiagnosticIOCGPtrConstant:    uint64(0x5678),
		}, config.diagnosticROData)
	})

	t.Run("observe uses production object and strict target", func(t *testing.T) {
		unsetIOCostRuntimeTestEnvironment(t)
		t.Setenv(ioCostRuntimeObjectEnvironment, ioCostRuntimeObserveObject)
		t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")
		t.Setenv(ioCostRuntimeObserveMinorEnvironment, "16")
		setIOCostRuntimeProtocolEnvironment(t)

		config, err := loadIOCostRuntimeConfig()
		require.NoError(t, err)
		require.False(t, config.diagnostic)
		require.True(t, config.observe)
		require.False(t, config.qualification)
		require.Equal(t, ioCostObjectName, config.objectName)
		require.Equal(t, ioCostRuntimeProductionObjectDirectory,
			config.objectDirectory)
		require.Equal(t, uint32(8), config.observeMajor)
		require.Equal(t, uint32(16), config.observeFirstMinor)
		require.NotEmpty(t, config.readyFile)
		require.NotEmpty(t, config.doneFile)
		require.NotEmpty(t, config.resultFile)
	})
}

func TestIOCostRuntimeObserveConfigRejectsInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*testing.T)
		wantSubstring string
	}{
		{
			name: "missing major",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeObserveMinorEnvironment, "16")
			},
			wantSubstring: ioCostRuntimeObserveMajorEnvironment + " is required",
		},
		{
			name: "invalid minor",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")
				t.Setenv(ioCostRuntimeObserveMinorEnvironment, "-1")
			},
			wantSubstring: "parse " + ioCostRuntimeObserveMinorEnvironment,
		},
		{
			name: "diagnostic input forbidden",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")
				t.Setenv(ioCostRuntimeObserveMinorEnvironment, "16")
				t.Setenv(ioCostRuntimeDiagnosticFaultMaskEnvironment, "0")
			},
			wantSubstring: ioCostRuntimeDiagnosticFaultMaskEnvironment,
		},
		{
			name: "missing protocol",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")
				t.Setenv(ioCostRuntimeObserveMinorEnvironment, "16")
			},
			wantSubstring: ioCostRuntimeReadyFileEnvironment + " is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsetIOCostRuntimeTestEnvironment(t)
			t.Setenv(ioCostRuntimeObjectEnvironment,
				ioCostRuntimeObserveObject)
			test.configure(t)
			if test.name != "missing protocol" {
				setIOCostRuntimeProtocolEnvironment(t)
			}

			_, err := loadIOCostRuntimeConfig()
			require.ErrorContains(t, err, test.wantSubstring)
		})
	}

	t.Run("observe input forbidden in diagnostic mode", func(t *testing.T) {
		unsetIOCostRuntimeTestEnvironment(t)
		t.Setenv(ioCostRuntimeObjectEnvironment,
			ioCostRuntimeDiagnosticObject)
		t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")

		_, err := loadIOCostRuntimeConfig()
		require.ErrorContains(t, err,
			ioCostRuntimeObserveMajorEnvironment)
	})

	t.Run("interactive input forbidden in production mode", func(t *testing.T) {
		unsetIOCostRuntimeTestEnvironment(t)
		t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")

		_, err := loadIOCostRuntimeConfig()
		require.ErrorContains(t, err,
			ioCostRuntimeObserveMajorEnvironment)
	})
}

func TestIOCostRuntimeObservationIdentityAndDrain(t *testing.T) {
	runtimeSession, _ := newIOCostRuntimeObservationTestSession(t, 2)

	drain, err := pollIOCostRuntimeDrain(t.Context(), runtimeSession)
	require.NoError(t, err)
	observation, err := readIOCostRuntimeObservation(
		t.Context(), runtimeSession, 8, 16)
	require.NoError(t, err)
	status, err := readIOCostRuntimeStatus(t.Context(), runtimeSession)
	require.NoError(t, err)
	require.Equal(t, ioCostStatus{}, status)
	require.Equal(t, ioCostRuntimeObservationResult{
		Major:         8,
		FirstMinor:    16,
		IOCPtr:        "0x100",
		IOCID:         "0x100000001",
		IOCGPtr:       "0x200",
		CSS:           "0x300",
		CSSSerial:     "0x400",
		IOCRows:       1,
		OwnerRows:     1,
		AggregateRows: 1,
	}, observation)
	require.Equal(t, ioCostRuntimeDrainResult{Drained: true}, drain)
}

func TestIOCostRuntimeObservationRejectsMapAndIdentityFailures(t *testing.T) {
	backendErr := errors.New("observe backend failure")
	tests := []struct {
		name          string
		drain         bool
		configure     func(*fakeIOCostCaptureBPF)
		wantSubstring string
	}{
		{
			name:  "missing drain map",
			drain: true,
			configure: func(object *fakeIOCostCaptureBPF) {
				object.removeMap(ioCostPendingMap)
			},
			wantSubstring: ioCostPendingMap + " is unavailable",
		},
		{
			name: "observation backend failure",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostOwnerStateMap,
					ioCostCaptureTestDump{err: backendErr})
			},
			wantSubstring: backendErr.Error(),
		},
		{
			name:  "duplicate drain map key",
			drain: true,
			configure: func(object *fakeIOCostCaptureBPF) {
				item := ioCostRuntimeObservationWakeItem(2)
				object.setDefaultItems(ioCostWakeFrameMap,
					[]bpf.MapItem{item, item})
			},
			wantSubstring: "duplicate " + ioCostWakeFrameMap + " key",
		},
		{
			name: "malformed aggregate",
			configure: func(object *fakeIOCostCaptureBPF) {
				item := object.defaultItems[ioCostWaitAggregateMap][0]
				item.Value = item.Value[:len(item.Value)-1]
				object.setDefaultItems(ioCostWaitAggregateMap,
					[]bpf.MapItem{item})
			},
			wantSubstring: "decode IOCOST observation aggregates",
		},
		{
			name: "target IOC absent",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
					ioCostCaptureTestIOCItem(0x100, 0x100000001, 9, 16),
				})
			},
			wantSubstring: "no IOC for target device 8:16",
		},
		{
			name: "target IOC ambiguous",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostIOCStateMap, []bpf.MapItem{
					ioCostCaptureTestIOCItem(0x100, 0x100000001, 8, 16),
					ioCostCaptureTestIOCItem(0x101, 0x100000002, 8, 16),
				})
			},
			wantSubstring: "multiple IOCs for target device 8:16",
		},
		{
			name: "target owner ambiguous",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.setDefaultItems(ioCostOwnerStateMap, []bpf.MapItem{
					ioCostCaptureTestOwnerItem(0x200, 0x100,
						0x100000001, 0x300, 0x400),
					ioCostCaptureTestOwnerItem(0x201, 0x100,
						0x100000001, 0x301, 0x401),
				})
			},
			wantSubstring: "multiple owners for target device 8:16",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
			test.configure(object)

			var err error
			if test.drain {
				_, err = pollIOCostRuntimeDrain(t.Context(), runtimeSession)
			} else {
				_, err = readIOCostRuntimeObservation(
					t.Context(), runtimeSession, 8, 16)
			}
			require.ErrorContains(t, err, test.wantSubstring)
		})
	}
}

func TestIOCostRuntimeDrainPollsUntilEmptyAndDeadline(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeIOCostCaptureBPF)
	}{
		{
			name: "pending becomes empty",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostPendingMap, ioCostCaptureTestDump{
					items: []bpf.MapItem{ioCostRuntimeObservationPendingItem()},
				})
			},
		},
		{
			name: "wake frame becomes inactive",
			configure: func(object *fakeIOCostCaptureBPF) {
				object.queueDumps(ioCostWakeFrameMap, ioCostCaptureTestDump{
					items: []bpf.MapItem{ioCostRuntimeObservationWakeItem(2, 1)},
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
			test.configure(object)

			drain, err := pollIOCostRuntimeDrainWithInterval(
				t.Context(), runtimeSession, time.Nanosecond)
			require.NoError(t, err)
			require.Equal(t, ioCostRuntimeDrainResult{Drained: true}, drain)
		})
	}

	t.Run("nonempty deadline", func(t *testing.T) {
		runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
		object.setDefaultItems(ioCostPendingMap,
			[]bpf.MapItem{ioCostRuntimeObservationPendingItem()})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()

		drain, err := pollIOCostRuntimeDrainWithInterval(
			ctx, runtimeSession, time.Hour)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, uint32(1), drain.PendingRows)
		require.False(t, drain.Drained)
	})
}

func TestIOCostRuntimeDiagnosticDrainDoesNotRequireIdentity(t *testing.T) {
	runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
	object.setDefaultItems(ioCostIOCStateMap, nil)
	object.setDefaultItems(ioCostOwnerStateMap, nil)
	object.setDefaultItems(ioCostWaitAggregateMap, nil)

	drain, err := pollIOCostRuntimeDrain(t.Context(), runtimeSession)
	require.NoError(t, err)
	require.Equal(t, ioCostRuntimeDrainResult{Drained: true}, drain)
}

func TestIOCostRuntimeObservationJSONUsesHexStrings(t *testing.T) {
	result := ioCostRuntimeResult{
		Observation: &ioCostRuntimeObservationResult{
			Major: 8, FirstMinor: 16,
			IOCPtr: "0x100", IOCID: "0x100000001",
			IOCGPtr: "0x200", CSS: "0x300", CSSSerial: "0x400",
			IOCRows: 1, OwnerRows: 1, AggregateRows: 2,
		},
		Drain: &ioCostRuntimeDrainResult{Drained: true},
	}
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	observation, ok := decoded["observation"].(map[string]any)
	require.True(t, ok)
	for name, want := range map[string]string{
		"ioc_ptr": "0x100", "ioc_id": "0x100000001",
		"iocg_ptr": "0x200", "css": "0x300", "css_serial": "0x400",
	} {
		require.Equal(t, want, observation[name], name)
	}
	drain, ok := decoded["drain"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, drain["drained"])

	legacy, err := json.Marshal(ioCostRuntimeResult{})
	require.NoError(t, err)
	require.NotContains(t, string(legacy), "observation")
	require.NotContains(t, string(legacy), `"drain"`)
}

func unsetIOCostRuntimeTestEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		ioCostRuntimeRequiredEnvironment,
		ioCostRuntimeObjectEnvironment,
		ioCostRuntimeDiagnosticFaultMaskEnvironment,
		ioCostRuntimeDiagnosticMajorEnvironment,
		ioCostRuntimeDiagnosticMinorEnvironment,
		ioCostRuntimeDiagnosticCSSSerialEnvironment,
		ioCostRuntimeDiagnosticIOCGPtrEnvironment,
		ioCostRuntimeObserveMajorEnvironment,
		ioCostRuntimeObserveMinorEnvironment,
		ioCostRuntimeReadyFileEnvironment,
		ioCostRuntimeDoneFileEnvironment,
		ioCostRuntimeResultFileEnvironment,
		ioCostRuntimeTimeoutEnvironment,
		ioCostRuntimeCheckpointEnvironment,
	} {
		value, present := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		t.Cleanup(func() {
			if present {
				require.NoError(t, os.Setenv(name, value))
				return
			}
			require.NoError(t, os.Unsetenv(name))
		})
	}
}

func setIOCostRuntimeProtocolEnvironment(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv(ioCostRuntimeReadyFileEnvironment,
		filepath.Join(directory, "ready"))
	t.Setenv(ioCostRuntimeDoneFileEnvironment,
		filepath.Join(directory, "done"))
	t.Setenv(ioCostRuntimeResultFileEnvironment,
		filepath.Join(directory, "result.json"))
}

func newIOCostRuntimeObservationTestSession(
	t *testing.T,
	possibleCPUs int,
) (*ioCostRuntimeSession, *fakeIOCostCaptureBPF) {
	t.Helper()
	object := newFakeIOCostCaptureBPF(possibleCPUs)
	object.mapIDs[ioCostPendingMap] = 5
	object.mapNames[5] = ioCostPendingMap
	object.mapIDs[ioCostWakeFrameMap] = 6
	object.mapNames[6] = ioCostWakeFrameMap
	object.defaultItems[ioCostPendingMap] = nil
	object.defaultItems[ioCostWakeFrameMap] = []bpf.MapItem{
		ioCostRuntimeObservationWakeItem(possibleCPUs),
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	session := &ioCostSession{
		object: object, possibleCPUs: possibleCPUs,
		breaker: ctx,
	}
	tracing := &iocostTracing{session: session}
	return &ioCostRuntimeSession{tracing: tracing, session: session}, object
}

func ioCostRuntimeObservationPendingItem() bpf.MapItem {
	value := make([]byte, ioCostPendingSize)
	binary.LittleEndian.PutUint64(value[0:8], 1)
	binary.LittleEndian.PutUint32(value[8:12], 1)
	return bpf.MapItem{Key: ioCostCaptureTestUint64(0x500), Value: value}
}

func ioCostRuntimeObservationWakeItem(
	possibleCPUs int,
	activeCPUs ...int,
) bpf.MapItem {
	value := make([]byte, possibleCPUs*ioCostWakeFrameSize)
	for _, cpu := range activeCPUs {
		offset := cpu * ioCostWakeFrameSize
		binary.LittleEndian.PutUint64(value[offset:offset+8], uint64(cpu+1))
		binary.LittleEndian.PutUint64(value[offset+8:offset+16], uint64(cpu+2))
		binary.LittleEndian.PutUint64(value[offset+16:offset+24], uint64(cpu+3))
		binary.LittleEndian.PutUint64(value[offset+24:offset+32], 1)
	}
	return bpf.MapItem{Key: make([]byte, ioCostUint32Size), Value: value}
}

// Qualification protocol and evidence validation.

// This file validates the IOCOST runtime qualification protocol and evidence.

func TestIOCostRuntimeQualificationConfigUsesProductionObjectAndProtocol(t *testing.T) {
	unsetIOCostRuntimeTestEnvironment(t)
	t.Setenv(ioCostRuntimeObjectEnvironment, ioCostRuntimeQualificationObject)
	t.Setenv(ioCostRuntimeTimeoutEnvironment, "300")
	setIOCostRuntimeProtocolEnvironment(t)

	config, err := loadIOCostRuntimeConfig()
	require.NoError(t, err)
	require.True(t, config.qualification)
	require.False(t, config.diagnostic)
	require.False(t, config.observe)
	require.Equal(t, ioCostObjectName, config.objectName)
	require.Equal(t, ioCostRuntimeProductionObjectDirectory,
		config.objectDirectory)
	require.Equal(t, 5*time.Minute, ioCostRuntimeMaximumTimeout)
	require.Equal(t, ioCostRuntimeMaximumTimeout, config.timeout)
	require.NotEmpty(t, config.readyFile)
	require.NotEmpty(t, config.doneFile)
	require.NotEmpty(t, config.resultFile)
}

func TestIOCostRuntimeQualificationConfigRejectsConflictsAndInvalidProtocol(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*testing.T)
		withProtocol  bool
		wantSubstring string
	}{
		{
			name: "diagnostic input",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeDiagnosticFaultMaskEnvironment, "0")
			},
			withProtocol:  true,
			wantSubstring: ioCostRuntimeDiagnosticFaultMaskEnvironment,
		},
		{
			name: "observe input",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeObserveMajorEnvironment, "8")
			},
			withProtocol:  true,
			wantSubstring: ioCostRuntimeObserveMajorEnvironment,
		},
		{
			name:          "missing protocol",
			configure:     func(*testing.T) {},
			wantSubstring: ioCostRuntimeReadyFileEnvironment + " is required",
		},
		{
			name: "timeout beyond maximum",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeTimeoutEnvironment, "301")
			},
			withProtocol:  true,
			wantSubstring: "exceeds 5m0s",
		},
		{
			name: "duplicate protocol path",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeDoneFileEnvironment,
					os.Getenv(ioCostRuntimeReadyFileEnvironment))
			},
			withProtocol:  true,
			wantSubstring: "resolve to the same path",
		},
		{
			name: "invalid checkpoint setting",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeCheckpointEnvironment, "0")
			},
			withProtocol:  true,
			wantSubstring: "requires qualification mode and value 1",
		},
		{
			name: "checkpoint path conflicts with final result",
			configure: func(t *testing.T) {
				t.Setenv(ioCostRuntimeCheckpointEnvironment, "1")
				t.Setenv(ioCostRuntimeResultFileEnvironment,
					os.Getenv(ioCostRuntimeDoneFileEnvironment)+".checkpoint")
			},
			withProtocol:  true,
			wantSubstring: "resolve to the same path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsetIOCostRuntimeTestEnvironment(t)
			t.Setenv(ioCostRuntimeObjectEnvironment,
				ioCostRuntimeQualificationObject)
			if test.withProtocol {
				setIOCostRuntimeProtocolEnvironment(t)
			}
			test.configure(t)

			_, err := loadIOCostRuntimeConfig()
			require.ErrorContains(t, err, test.wantSubstring)
		})
	}
}

func TestIOCostRuntimeQualificationCheckpointDoesNotConsumeInterval(t *testing.T) {
	unsetIOCostRuntimeTestEnvironment(t)
	t.Setenv(ioCostRuntimeObjectEnvironment, ioCostRuntimeQualificationObject)
	t.Setenv(ioCostRuntimeCheckpointEnvironment, "1")
	setIOCostRuntimeProtocolEnvironment(t)
	config, err := loadIOCostRuntimeConfig()
	require.NoError(t, err)
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile+".checkpoint", []byte("ready\n")))
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))

	updates, observations := 0, 0
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updates++
			if updates == 2 {
				require.FileExists(t, config.resultFile+".checkpoint",
					"the checkpoint must precede the first business Update")
			}
			return nil, nil
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			return ioCostRuntimeDrainResult{Drained: true}, nil
		},
		mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
			observations++
			if observations == 1 {
				require.Equal(t, 1, updates, "checkpoint must retain the empty baseline")
			}
			return ioCostRuntimeObservationResult{IOCRows: uint32(observations)}, nil
		},
		laneObservation: func(context.Context) (ioCostRuntimeLaneObservationResult, error) {
			return newIOCostRuntimeLaneObservationResult(), nil
		},
		status: func(context.Context) (ioCostStatus, error) { return ioCostStatus{}, nil },
	}
	result, err := runIOCostRuntimeQualification(t.Context(), config, steps)
	require.NoError(t, err)
	require.Equal(t, 3, updates)
	require.Equal(t, 2, observations)
	require.Equal(t, uint32(2), result.Observation.IOCRows)
	data, err := os.ReadFile(config.resultFile + ".checkpoint")
	require.NoError(t, err)
	var checkpoint ioCostRuntimeResult
	require.NoError(t, json.Unmarshal(data, &checkpoint))
	require.Equal(t, uint32(1), checkpoint.Observation.IOCRows)
	require.True(t, checkpoint.Drain.Drained)
	require.Nil(t, checkpoint.BusinessIntervals)
}

func TestIOCostRuntimeQualificationProtocolCapturesTwoBusinessIntervals(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))

	events := make([]string, 0, 8)
	updateCalls := 0
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updateCalls++
			switch updateCalls {
			case 1:
				events = append(events, "baseline")
				_, err := os.Lstat(config.readyFile)
				require.ErrorIs(t, err, os.ErrNotExist)
				return []*metric.Data{}, nil
			case 2:
				events = append(events, "first")
				return []*metric.Data{metric.NewGaugeData(
					ioCostWaitCountName,
					3,
					ioCostWaitCountHelp,
					map[string]string{
						"scope": "host", "operation": "write", "device": "7:9",
					},
				)}, nil
			case 3:
				events = append(events, "second")
				return []*metric.Data{metric.NewGaugeData(
					ioCostWaitCountName,
					0,
					ioCostWaitCountHelp,
					map[string]string{
						"scope": "host", "operation": "write", "device": "7:9",
					},
				)}, nil
			default:
				t.Fatalf("unexpected qualification Update %d", updateCalls)
				return nil, nil
			}
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			events = append(events, "drain")
			ready, err := os.ReadFile(config.readyFile)
			require.NoError(t, err)
			require.Equal(t, "ready\n", string(ready))
			return ioCostRuntimeDrainResult{Drained: true}, nil
		},
		mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
			events = append(events, "map rows")
			return ioCostRuntimeObservationResult{
				IOCRows: 1, OwnerRows: 1, AggregateRows: 1,
			}, nil
		},
		laneObservation: func(context.Context) (
			ioCostRuntimeLaneObservationResult,
			error,
		) {
			events = append(events, "lanes")
			return ioCostRuntimeLaneObservationResult{
				CountLanes: []int{1, 4},
				WaitLanes:  []int{4},
				RawSeries: []ioCostRuntimeRawSeriesResult{{
					Device:    "7:9",
					Operation: "write",
					IOCount:   "3",
					Wait10US:  "900",
				}},
			}, nil
		},
		status: func(context.Context) (ioCostStatus, error) {
			events = append(events, "status")
			return ioCostStatus{}, nil
		},
	}

	result, err := runIOCostRuntimeQualification(
		t.Context(), config, steps)
	require.NoError(t, err)
	require.Equal(t, []string{
		"baseline", "drain", "map rows", "lanes", "first", "second", "status",
	}, events)
	require.Equal(t, &ioCostRuntimeDrainResult{Drained: true}, result.Drain)
	require.Equal(t, &ioCostRuntimeObservationResult{
		IOCRows: 1, OwnerRows: 1, AggregateRows: 1,
	}, result.Observation)
	require.Equal(t, &ioCostRuntimeLaneObservationResult{
		CountLanes: []int{1, 4},
		WaitLanes:  []int{4},
		RawSeries: []ioCostRuntimeRawSeriesResult{{
			Device:    "7:9",
			Operation: "write",
			IOCount:   "3",
			Wait10US:  "900",
		}},
	}, result.LaneObservation)
	require.Equal(t, ioCostRuntimeStatusResult{}, result.Status)
	require.NotNil(t, result.BusinessIntervals)
	require.Len(t, result.BusinessIntervals.First, 1)
	require.Len(t, result.BusinessIntervals.Second, 1)
	require.Equal(t, float64(3), result.BusinessIntervals.First[0].Value)
	require.Equal(t, float64(0), result.BusinessIntervals.Second[0].Value)
	require.Equal(t, ioCostWaitCountName,
		result.BusinessIntervals.First[0].Name)
	require.Equal(t, []string{
		metric.LabelRegion,
		metric.LabelHost,
		"device",
		"operation",
		"scope",
	}, ioCostRuntimeQualificationLabelNames(
		result.BusinessIntervals.First[0].Labels))
}

func TestIOCostRuntimeQualificationRequiresEmptyBaselineBeforeReady(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			return []*metric.Data{metric.NewGaugeData(
				ioCostWaitCountName, 1, ioCostWaitCountHelp, nil)}, nil
		},
	}

	result, err := runIOCostRuntimeQualification(
		t.Context(), config, steps)
	require.ErrorContains(t, err, "baseline Update returned 1 IOCOST metrics")
	require.NotNil(t, result.BusinessIntervals)
	require.Equal(t,
		&ioCostRuntimeLaneObservationResult{
			CountLanes: []int{},
			WaitLanes:  []int{},
			RawSeries:  []ioCostRuntimeRawSeriesResult{},
		},
		result.LaneObservation,
		"qualification results must retain the additive lane field on failure",
	)
	_, readyErr := os.Lstat(config.readyFile)
	require.ErrorIs(t, readyErr, os.ErrNotExist)
}

func TestIOCostRuntimeQualificationStopsAtFailedBoundary(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))

	updateCalls := 0
	statusCalled := false
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updateCalls++
			if updateCalls == 1 {
				return []*metric.Data{}, nil
			}
			return nil, errors.New("first interval failure")
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			return ioCostRuntimeDrainResult{Drained: true}, nil
		},
		mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
			return ioCostRuntimeObservationResult{}, nil
		},
		laneObservation: func(context.Context) (
			ioCostRuntimeLaneObservationResult,
			error,
		) {
			return newIOCostRuntimeLaneObservationResult(), nil
		},
		status: func(context.Context) (ioCostStatus, error) {
			statusCalled = true
			return ioCostStatus{}, nil
		},
	}

	result, err := runIOCostRuntimeQualification(
		t.Context(), config, steps)
	require.ErrorContains(t, err, "first IOCOST business Update")
	require.Equal(t, 2, updateCalls)
	require.False(t, statusCalled)
	require.Equal(t, &ioCostRuntimeDrainResult{Drained: true}, result.Drain)
	require.NotNil(t, result.BusinessIntervals)
	require.Empty(t, result.BusinessIntervals.First)
	require.Empty(t, result.BusinessIntervals.Second)
}

func TestIOCostRuntimeQualificationTakesSecondUpdateBeforeEncoding(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))

	updateCalls := 0
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updateCalls++
			switch updateCalls {
			case 1:
				return []*metric.Data{}, nil
			case 2:
				return []*metric.Data{
					metric.NewGaugeData("foreign", 1, "foreign", nil),
				}, nil
			case 3:
				return []*metric.Data{metric.NewGaugeData(
					ioCostWaitCountName, 0, ioCostWaitCountHelp, nil)}, nil
			default:
				t.Fatalf("unexpected qualification Update %d", updateCalls)
				return nil, nil
			}
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			return ioCostRuntimeDrainResult{Drained: true}, nil
		},
		mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
			return ioCostRuntimeObservationResult{}, nil
		},
		laneObservation: func(context.Context) (
			ioCostRuntimeLaneObservationResult,
			error,
		) {
			return newIOCostRuntimeLaneObservationResult(), nil
		},
		status: func(context.Context) (ioCostStatus, error) {
			t.Fatal("status must follow successful interval encoding")
			return ioCostStatus{}, nil
		},
	}

	result, err := runIOCostRuntimeQualification(t.Context(), config, steps)
	require.ErrorContains(t, err, "encode first IOCOST business interval")
	require.Equal(t, 3, updateCalls,
		"the duplicate-detection Update must immediately follow the first Update")
	require.Len(t, result.BusinessIntervals.Second, 1,
		"a first-interval encoding failure must retain the second interval")
	require.Zero(t, result.BusinessIntervals.Second[0].Value)
}

func TestIOCostRuntimeQualificationRetainsFirstIntervalWhenSecondUpdateFails(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))

	updateCalls := 0
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updateCalls++
			switch updateCalls {
			case 1:
				return []*metric.Data{}, nil
			case 2:
				return []*metric.Data{metric.NewGaugeData(
					ioCostWaitCountName, 4, ioCostWaitCountHelp, nil)}, nil
			case 3:
				return nil, errors.New("second interval failure")
			default:
				t.Fatalf("unexpected qualification Update %d", updateCalls)
				return nil, nil
			}
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			return ioCostRuntimeDrainResult{Drained: true}, nil
		},
		mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
			return ioCostRuntimeObservationResult{}, nil
		},
		laneObservation: func(context.Context) (
			ioCostRuntimeLaneObservationResult,
			error,
		) {
			return newIOCostRuntimeLaneObservationResult(), nil
		},
		status: func(context.Context) (ioCostStatus, error) {
			t.Fatal("status must follow two successful business Updates")
			return ioCostStatus{}, nil
		},
	}

	result, err := runIOCostRuntimeQualification(t.Context(), config, steps)
	require.ErrorContains(t, err, "second IOCOST business Update")
	require.Len(t, result.BusinessIntervals.First, 1,
		"a second-Update failure must retain the captured first interval")
	require.Equal(t, float64(4), result.BusinessIntervals.First[0].Value)
	require.Empty(t, result.BusinessIntervals.Second)
}

func TestIOCostRuntimeQualificationDoneWaitHonorsContext(t *testing.T) {
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	drainCalled := false
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			return []*metric.Data{}, nil
		},
		drain: func(context.Context) (ioCostRuntimeDrainResult, error) {
			drainCalled = true
			return ioCostRuntimeDrainResult{}, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runIOCostRuntimeQualification(ctx, config, steps)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, drainCalled)
	ready, readErr := os.ReadFile(config.readyFile)
	require.NoError(t, readErr)
	require.Equal(t, "ready\n", string(ready))
}

func TestIOCostRuntimeQualificationLaneBoundaryFailsClosedAndRetainsResult(
	t *testing.T,
) {
	observationErr := errors.New("lane capture failure")
	tests := []struct {
		name            string
		laneObservation func(context.Context) (
			ioCostRuntimeLaneObservationResult,
			error,
		)
		wantError       string
		wantObservation ioCostRuntimeLaneObservationResult
	}{
		{
			name:            "missing callback",
			wantError:       "lane observation is unavailable",
			wantObservation: newIOCostRuntimeLaneObservationResult(),
		},
		{
			name: "capture error",
			laneObservation: func(context.Context) (
				ioCostRuntimeLaneObservationResult,
				error,
			) {
				return ioCostRuntimeLaneObservationResult{
					CountLanes: []int{3},
					WaitLanes:  make([]int, 0),
					RawSeries:  make([]ioCostRuntimeRawSeriesResult, 0),
				}, observationErr
			},
			wantError: "capture IOCOST qualification lane observation: " +
				observationErr.Error(),
			wantObservation: ioCostRuntimeLaneObservationResult{
				CountLanes: []int{3},
				WaitLanes:  make([]int, 0),
				RawSeries:  make([]ioCostRuntimeRawSeriesResult, 0),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			config := ioCostRuntimeConfig{
				readyFile: filepath.Join(directory, "ready"),
				doneFile:  filepath.Join(directory, "done"),
			}
			require.NoError(t, writeIOCostRuntimeFile(
				config.doneFile, []byte("done\n")))

			updateCalls := 0
			statusCalled := false
			steps := ioCostRuntimeQualificationSteps{
				update: func() ([]*metric.Data, error) {
					updateCalls++
					return []*metric.Data{}, nil
				},
				drain: func(context.Context) (
					ioCostRuntimeDrainResult,
					error,
				) {
					return ioCostRuntimeDrainResult{Drained: true}, nil
				},
				mapRows: func(context.Context) (ioCostRuntimeObservationResult, error) {
					return ioCostRuntimeObservationResult{}, nil
				},
				laneObservation: test.laneObservation,
				status: func(context.Context) (ioCostStatus, error) {
					statusCalled = true
					return ioCostStatus{}, nil
				},
			}

			result, err := runIOCostRuntimeQualification(
				t.Context(), config, steps)
			require.ErrorContains(t, err, test.wantError)
			require.Equal(t, 1, updateCalls,
				"the failed lane boundary must precede the first business Update")
			require.False(t, statusCalled)
			require.Equal(t,
				&ioCostRuntimeDrainResult{Drained: true}, result.Drain)
			require.NotNil(t, result.LaneObservation)
			require.Equal(t, test.wantObservation, *result.LaneObservation)
			require.NotNil(t, result.BusinessIntervals)
			require.Empty(t, result.BusinessIntervals.First)
			require.Empty(t, result.BusinessIntervals.Second)
		})
	}
}

func TestIOCostRuntimeQualificationMapRowsRetainOrphanAggregate(t *testing.T) {
	runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 1)
	live := object.defaultItems[ioCostWaitAggregateMap][0]
	object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
		live,
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID+1, ioCostCaptureTestCSSSerial+1, 0, 0,
			ioCostCaptureStableLane(99, 999),
		),
	})

	observation, err := readIOCostRuntimeMapRows(t.Context(), runtimeSession)
	require.NoError(t, err)
	require.Equal(t, ioCostRuntimeObservationResult{
		IOCRows: 1, OwnerRows: 1, AggregateRows: 2,
		IOCs: map[string]string{"0x100000001": "8:16"},
	}, observation)
	lanes, err := captureIOCostRuntimeLaneObservation(t.Context(), runtimeSession)
	require.NoError(t, err)
	require.Equal(t, []ioCostRuntimeRawSeriesResult{{
		Device: "8:16", Operation: "read", IOCount: "1", Wait10US: "1000",
	}}, lanes.RawSeries,
		"the public join must omit the orphan that raw row evidence retains")
	require.Nil(t, runtimeSession.session.previous,
		"raw row evidence must not commit the collection baseline")
}

func TestIOCostRuntimeQualificationStopsAtRawMapReadFailure(t *testing.T) {
	runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 1)
	backendErr := errors.New("raw map rows unavailable")
	object.queueDumps(ioCostWaitAggregateMap,
		ioCostCaptureTestDump{err: backendErr})
	directory := t.TempDir()
	config := ioCostRuntimeConfig{
		readyFile: filepath.Join(directory, "ready"),
		doneFile:  filepath.Join(directory, "done"),
	}
	require.NoError(t, writeIOCostRuntimeFile(config.doneFile, []byte("done\n")))
	updates := 0
	steps := ioCostRuntimeQualificationSteps{
		update: func() ([]*metric.Data, error) {
			updates++
			return nil, nil
		},
		drain: func(ctx context.Context) (ioCostRuntimeDrainResult, error) {
			return pollIOCostRuntimeDrain(ctx, runtimeSession)
		},
		mapRows: func(ctx context.Context) (ioCostRuntimeObservationResult, error) {
			return readIOCostRuntimeMapRows(ctx, runtimeSession)
		},
	}

	result, err := runIOCostRuntimeQualification(t.Context(), config, steps)
	require.ErrorIs(t, err, backendErr)
	require.Equal(t, 1, updates,
		"unreadable raw rows must stop qualification before business Updates")
	require.Equal(t, &ioCostRuntimeDrainResult{Drained: true}, result.Drain)
	require.NotNil(t, result.Observation)
	require.Empty(t, result.BusinessIntervals.First)
	require.Empty(t, result.BusinessIntervals.Second)
}

func TestIOCostRuntimeLaneObservationUnionsLanesAndAggregatesRawSeries(
	t *testing.T,
) {
	snapshot := newIOCostRawSnapshot()
	snapshot.Samples[ioCostWaitKey{
		IOCID: 1, CSSSerial: 10, Operation: 0,
	}] = ioCostRawSample{
		CSS:       0xaaa,
		Device:    "9:0",
		Operation: "read",
		Counters: []ioCostCumulative{
			{IOCount: 2, Wait10US: 20},
			{IOCount: 3},
			{},
		},
	}
	snapshot.Samples[ioCostWaitKey{
		IOCID: 1, CSSSerial: 20, Operation: 0,
	}] = ioCostRawSample{
		CSS:       0xbbb,
		Device:    "9:0",
		Operation: "read",
		Counters: []ioCostCumulative{
			{IOCount: 5, Wait10US: 50},
			{},
			{IOCount: 7, Wait10US: 70},
		},
	}
	snapshot.Samples[ioCostWaitKey{
		IOCID: 2, CSSSerial: 30, Operation: 1,
	}] = ioCostRawSample{
		CSS:       0xccc,
		Device:    "10:0",
		Operation: "write",
		Counters: []ioCostCumulative{
			{IOCount: 9, Wait10US: 90},
			{},
			{IOCount: 11},
		},
	}
	snapshot.Samples[ioCostWaitKey{
		IOCID: 1, CSSSerial: 40, Operation: 1,
	}] = ioCostRawSample{
		CSS:       0xddd,
		Device:    "9:0",
		Operation: "write",
		Counters: []ioCostCumulative{
			{},
			{IOCount: 13, Wait10US: 130},
			{},
		},
	}

	observation, err := makeIOCostRuntimeLaneObservation(snapshot, 3)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2}, observation.CountLanes)
	require.Equal(t, []int{0, 1, 2}, observation.WaitLanes)
	require.Equal(t, []ioCostRuntimeRawSeriesResult{
		{
			Device:    "10:0",
			Operation: "write",
			IOCount:   "20",
			Wait10US:  "90",
		},
		{
			Device:    "9:0",
			Operation: "read",
			IOCount:   "17",
			Wait10US:  "140",
		},
		{
			Device:    "9:0",
			Operation: "write",
			IOCount:   "13",
			Wait10US:  "130",
		},
	}, observation.RawSeries)

	empty, err := makeIOCostRuntimeLaneObservation(newIOCostRawSnapshot(), 3)
	require.NoError(t, err)
	require.NotNil(t, empty.CountLanes)
	require.NotNil(t, empty.WaitLanes)
	require.NotNil(t, empty.RawSeries)
	require.Empty(t, empty.CountLanes)
	require.Empty(t, empty.WaitLanes)
	require.Empty(t, empty.RawSeries)
}

func TestIOCostRuntimeLaneObservationRejectsABIAndSessionFailures(t *testing.T) {
	t.Run("raw wait without IO", func(t *testing.T) {
		snapshot := newIOCostRawSnapshot()
		snapshot.Samples[ioCostWaitKey{
			IOCID: 1, CSSSerial: 2,
		}] = ioCostRawSample{
			Device:    "8:0",
			Operation: "read",
			Counters:  []ioCostCumulative{{Wait10US: 1}},
		}

		observation, err := makeIOCostRuntimeLaneObservation(snapshot, 1)
		require.ErrorContains(t, err, "wait_10us 1 with no IO")
		require.Equal(t,
			newIOCostRuntimeLaneObservationResult(), observation)
	})

	t.Run("raw capture backend error", func(t *testing.T) {
		runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
		backendErr := errors.New("lane backend failure")
		object.queueDumps(ioCostWaitAggregateMap,
			ioCostCaptureTestDump{err: backendErr})

		observation, err := captureIOCostRuntimeLaneObservation(
			t.Context(), runtimeSession)
		require.ErrorContains(t, err, backendErr.Error())
		require.Equal(t,
			newIOCostRuntimeLaneObservationResult(), observation)
	})

	t.Run("raw map ABI", func(t *testing.T) {
		runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 2)
		item := object.defaultItems[ioCostWaitAggregateMap][0]
		item.Value = item.Value[:len(item.Value)-1]
		object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})

		observation, err := captureIOCostRuntimeLaneObservation(
			t.Context(), runtimeSession)
		require.ErrorContains(t, err, "decode "+ioCostWaitAggregateMap)
		require.Equal(t,
			newIOCostRuntimeLaneObservationResult(), observation)
	})

	t.Run("raw ABI lane count", func(t *testing.T) {
		snapshot := newIOCostRawSnapshot()
		snapshot.Samples[ioCostWaitKey{
			IOCID: 1, CSSSerial: 2,
		}] = ioCostRawSample{Counters: []ioCostCumulative{{}}}

		observation, err := makeIOCostRuntimeLaneObservation(snapshot, 2)
		require.ErrorContains(t, err, "has 1 CPU lanes, want 2")
		require.Equal(t,
			newIOCostRuntimeLaneObservationResult(), observation)
	})

	t.Run("withdrawn session under collector lock", func(t *testing.T) {
		retained := &ioCostSession{}
		runtimeSession := &ioCostRuntimeSession{
			tracing: &iocostTracing{},
			session: retained,
		}

		observation, err := captureIOCostRuntimeLaneObservation(
			t.Context(), runtimeSession)
		require.ErrorContains(t, err, "session was withdrawn")
		require.Equal(t,
			newIOCostRuntimeLaneObservationResult(), observation)
	})
}

func TestIOCostRuntimeLaneObservationCapturesRawSessionWithoutUpdate(
	t *testing.T,
) {
	runtimeSession, object := newIOCostRuntimeObservationTestSession(t, 3)
	object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestIOCID,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			ioCostCaptureStableLane(1, 7),
			ioCostCaptureStableLane(5, 0),
			ioCostCaptureStableLane(6, 8),
		),
	})

	observation, err := captureIOCostRuntimeLaneObservation(
		t.Context(), runtimeSession)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2}, observation.CountLanes)
	require.Equal(t, []int{0, 2}, observation.WaitLanes)
	require.Equal(t, []ioCostRuntimeRawSeriesResult{{
		Device:    "8:16",
		Operation: "read",
		IOCount:   "12",
		Wait10US:  "15",
	}}, observation.RawSeries)
	require.Nil(t, runtimeSession.session.previous,
		"lane evidence must not commit the collection baseline")
}

func TestIOCostRuntimeQualificationMetricExtractionIsExactAndOrdered(t *testing.T) {
	labels := map[string]string{
		"scope": "other", "operation": "read", "device": "8:16",
	}
	data := []*metric.Data{
		metric.NewGaugeData(ioCostWaitCountName, 7, ioCostWaitCountHelp, labels),
		metric.NewGaugeData(ioCostAverageWaitName, 1.25, ioCostAverageWaitHelp, labels),
	}

	result, err := makeIOCostRuntimeMetricResults(data)
	require.NoError(t, err)
	require.Equal(t, []string{
		ioCostWaitCountName,
		ioCostAverageWaitName,
	}, []string{result[0].Name, result[1].Name})
	require.Equal(t, []float64{7, 1.25},
		[]float64{result[0].Value, result[1].Value})
	require.Equal(t, []string{
		metric.LabelRegion,
		metric.LabelHost,
		"device",
		"operation",
		"scope",
	}, ioCostRuntimeQualificationLabelNames(result[0].Labels))
	require.Equal(t, []string{"8:16", "read", "other"}, []string{
		result[0].Labels[2].Value,
		result[0].Labels[3].Value,
		result[0].Labels[4].Value,
	})

	tests := []struct {
		name          string
		data          []*metric.Data
		wantSubstring string
	}{
		{
			name:          "nil metric",
			data:          []*metric.Data{nil},
			wantSubstring: "metric 0: is nil",
		},
		{
			name: "counter type",
			data: []*metric.Data{metric.NewCounterData(
				ioCostWaitCountName, 1, ioCostWaitCountHelp, labels)},
			wantSubstring: "is not a gauge",
		},
		{
			name: "foreign metric",
			data: []*metric.Data{metric.NewGaugeData(
				"not_iocost", 1, "foreign", labels)},
			wantSubstring: "unexpected IOCOST metric name",
		},
		{
			name: "non-finite value",
			data: []*metric.Data{metric.NewGaugeData(
				ioCostWaitCountName, math.Inf(1), ioCostWaitCountHelp, labels)},
			wantSubstring: "non-finite value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := makeIOCostRuntimeMetricResults(test.data)
			require.ErrorContains(t, err, test.wantSubstring)
		})
	}
}

func TestIOCostRuntimeQualificationJSONIsAdditiveAndMachineDecodable(t *testing.T) {
	result := ioCostRuntimeResult{
		BusinessIntervals: &ioCostRuntimeBusinessIntervalsResult{
			First: []ioCostRuntimeMetricResult{{
				Name:  ioCostWaitCountName,
				Value: 2,
				Labels: []ioCostRuntimeMetricLabelResult{
					{Name: "device", Value: "8:16"},
					{Name: "operation", Value: "read"},
					{Name: "scope", Value: "host"},
				},
			}},
			Second: []ioCostRuntimeMetricResult{},
		},
		Drain: &ioCostRuntimeDrainResult{Drained: true},
		LaneObservation: &ioCostRuntimeLaneObservationResult{
			CountLanes: []int{0, 3},
			WaitLanes:  []int{3},
			RawSeries: []ioCostRuntimeRawSeriesResult{{
				Device:    "8:16",
				Operation: "read",
				IOCount:   "9007199254740993",
				Wait10US:  "18446744073709551615",
			}},
		},
	}
	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded struct {
		BusinessIntervals struct {
			First  []ioCostRuntimeMetricResult `json:"first"`
			Second []ioCostRuntimeMetricResult `json:"second"`
		} `json:"business_intervals"`
		LaneObservation ioCostRuntimeLaneObservationResult `json:"lane_observation"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.BusinessIntervals.First, 1)
	require.NotNil(t, decoded.BusinessIntervals.Second)
	require.Empty(t, decoded.BusinessIntervals.Second)
	require.Equal(t, "device",
		decoded.BusinessIntervals.First[0].Labels[0].Name)
	require.Equal(t, []int{0, 3}, decoded.LaneObservation.CountLanes)
	require.Equal(t, []int{3}, decoded.LaneObservation.WaitLanes)
	require.Equal(t, []ioCostRuntimeRawSeriesResult{{
		Device:    "8:16",
		Operation: "read",
		IOCount:   "9007199254740993",
		Wait10US:  "18446744073709551615",
	}}, decoded.LaneObservation.RawSeries)

	legacy, err := json.Marshal(ioCostRuntimeResult{})
	require.NoError(t, err)
	require.NotContains(t, string(legacy), "business_intervals")
	require.NotContains(t, string(legacy), "lane_observation")
}

func ioCostRuntimeQualificationLabelNames(
	labels []ioCostRuntimeMetricLabelResult,
) []string {
	names := make([]string, len(labels))
	for index, label := range labels {
		names[index] = label.Name
	}
	return names
}
