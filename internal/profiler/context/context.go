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

package context

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/output"
	_ "huatuo-bamai/internal/profiler/output/flamegraph"
	_ "huatuo-bamai/internal/profiler/output/raw"
	psignal "huatuo-bamai/internal/profiler/signal"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/profiling"

	"github.com/urfave/cli/v2"
)

type ProfilerContext struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Cli    *cli.Context

	PIDs                 []int
	Freq                 int
	Duration             int
	MaxProfilerProcesses int
	AggrInterval         int
	IsOneShotAgg         bool
	CPUIDs               []int
	CgroupID             uint64
	ProcessGroupID       int
	LockMinWait          time.Duration

	ServerAddress             string
	OutputFormat              output.OutputFormat
	OutputPath                string
	ContainerID               string
	Type                      profiling.Type
	Language                  profiling.Language
	ExecPath                  string
	Scope                     string
	ToolPath                  string
	LogBpfDebug               bool
	MemoryMode                profiling.MemoryMode
	PhysicalMemoryProbability uint
	CgroupPath                string
	LockMode                  string
	LockTypes                 []string
	Labels                    map[string]string

	TracerID string

	ToolstreamClient *toolstream.Client
}

type TracerData struct {
	MetricData any                   `json:"metric_data,omitempty"`
	FlameData  *profiler.ProfileData `json:"flamedata"`
}

func NewProfilerContext(cliCtx *cli.Context, logBuf *bytes.Buffer) (*ProfilerContext, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)

	sigCh, err := psignal.SetupSignals()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to setup signals: %w", err)
	}
	var cancelOnce sync.Once
	listenerDone := make(chan struct{})
	cancelProfiler := func() {
		cancelOnce.Do(func() {
			psignal.StopSignals(sigCh)
			cancel()
			<-listenerDone
		})
	}
	succeeded := false
	var tsClient *toolstream.Client
	defer func() {
		if succeeded {
			return
		}
		cancelProfiler()
		if tsClient != nil {
			_ = tsClient.Close()
		}
	}()

	go func() {
		defer close(listenerDone)
		sig, err := psignal.ListenSignalAndCancel(ctx, sigCh, cancel)
		if err != nil {
			fmt.Fprintf(logBuf, "[signal] error: %v\n", err)
		}
		if sig != nil {
			fmt.Fprintf(logBuf, "[signal] caught signal: %s, canceling context\n", sig)
		}
	}()

	labels, err := parseProfileLabels(cliCtx.StringSlice("label"))
	if err != nil {
		return nil, err
	}
	for name := range labels {
		if err := profiler.ValidateCustomLabelName(name); err != nil {
			return nil, err
		}
	}
	outputFormat := output.OutputFormat(cliCtx.String("output-format"))

	tsClient, err = initToolstreamClient(cliCtx, outputFormat)
	if err != nil {
		return nil, err
	}

	var cpuIDs []int
	if cpuidStr := cliCtx.String("cpuid"); cpuidStr != "" {
		cpuIDs, err = parseCPUIDList(cpuidStr)
		if err != nil {
			return nil, err
		}
	}

	pids, err := ParsePIDs(cliCtx.String("pid"))
	if err != nil {
		return nil, err
	}
	typ, err := profiling.ParseType(cliCtx.String("type"))
	if err != nil {
		return nil, err
	}
	language, err := profiling.ParseLanguage(cliCtx.String("language"))
	if err != nil {
		return nil, err
	}
	mode := profiling.MemoryModeUnknown
	if cliCtx.String("memory-mode") != "" {
		mode, err = profiling.ParseMemoryMode(cliCtx.String("memory-mode"))
		if err != nil {
			return nil, err
		}
	}

	targetPID := 0
	if len(pids) > 0 {
		targetPID = pids[0]
	}

	scope := cliCtx.String("scope")
	cgroupID := cliCtx.Uint64("cgroup-id")
	cgroupPath := strings.TrimSpace(cliCtx.String("cgroup-path"))
	processGroupID := cliCtx.Int("process-group-id")
	lockTypes := []string(nil)

	implementation, _ := profiling.ImplementationFor(language)
	if implementation == profiling.ImplementationNative {
		scope, err = normalizeRequestedScope(scope, cliCtx.IsSet("scope"), targetPID)
		if err != nil {
			return nil, err
		}

		if cgroupPath != "" && cgroupID == 0 {
			var stat syscall.Stat_t
			if err := syscall.Stat(cgroupPath, &stat); err != nil {
				return nil, fmt.Errorf("stat cgroup path %q: %w", cgroupPath, err)
			}
			cgroupID = stat.Ino
		}

		if scope == ScopeProcessGroup && processGroupID == 0 && targetPID != 0 {
			if len(pids) > 1 {
				return nil, fmt.Errorf("scope process-group cannot derive one process group from multiple PIDs")
			}
			processGroupID, err = syscall.Getpgid(targetPID)
			if err != nil {
				return nil, fmt.Errorf("resolve process group for pid %d: %w", targetPID, err)
			}
		}

		scope = inferImplicitScope(
			scope,
			cliCtx.IsSet("scope"),
			targetPID,
			processGroupID,
			cgroupID,
			cliCtx.String("container-id"),
		)

		labels[profiler.LabelProfilingScope] = scope
		if containerID := cliCtx.String("container-id"); containerID != "" {
			// Container CSS filtering is applied in addition to the selected scope,
			// so preserve it as a dimension even for an explicit PID/TGID target.
			labels[profiler.LabelContainerID] = containerID
		}
		switch scope {
		case ScopePID:
			if len(pids) > 0 {
				labels[profiler.LabelPID] = formatPIDs(pids)
			}
		case ScopeTGID:
			if len(pids) > 0 {
				labels[profiler.LabelTGID] = formatPIDs(pids)
			}
		case ScopeCgroup:
			if cgroupID != 0 {
				labels[profiler.LabelCgroupID] = strconv.FormatUint(cgroupID, 10)
			}
			if cgroupPath != "" {
				labels[profiler.LabelCgroupPath] = cgroupPath
			}
		case ScopeProcessGroup:
			if processGroupID != 0 {
				labels[profiler.LabelProcessGroupID] = strconv.Itoa(processGroupID)
			}
		}
	}

	if typ == profiling.TypeLock {
		lockTypes, err = ParseLockTypes(cliCtx.String("lock-types"))
		if err != nil {
			return nil, err
		}
		// lock_type is a queryable series label only when it identifies every
		// sample in the profile. Multi-type profiles keep the real type in each
		// stack prefix instead of publishing a misleading comma-joined label.
		if len(lockTypes) == 1 {
			labels[profiler.LabelLockType] = lockTypes[0]
		}
	}

	profilerContext := &ProfilerContext{
		Ctx:    ctx,
		Cancel: cancelProfiler,
		Cli:    cliCtx,

		PIDs:                 pids,
		Freq:                 cliCtx.Int("freq"),
		Duration:             cliCtx.Int("duration"),
		MaxProfilerProcesses: cliCtx.Int("max-concurrent-procs"),
		AggrInterval:         cliCtx.Int("aggr-interval"),
		CPUIDs:               cpuIDs,
		CgroupID:             cgroupID,
		ProcessGroupID:       processGroupID,
		LockMinWait:          cliCtx.Duration("lock-min-wait"),

		ServerAddress:             cliCtx.String("huatuo-api-address"),
		Type:                      typ,
		Language:                  language,
		ContainerID:               cliCtx.String("container-id"),
		ExecPath:                  cliCtx.String("binary-match-path"),
		Scope:                     scope,
		ToolPath:                  cliCtx.String("tool-path"),
		LogBpfDebug:               cliCtx.Bool("log-bpf-debug"),
		OutputPath:                cliCtx.String("output-path"),
		OutputFormat:              outputFormat,
		MemoryMode:                mode,
		PhysicalMemoryProbability: cliCtx.Uint("physical-memory-probability"),
		CgroupPath:                cgroupPath,
		LockMode:                  cliCtx.String("lock-mode"),
		LockTypes:                 lockTypes,
		Labels:                    labels,

		TracerID: cliCtx.String("tracer-id"),

		ToolstreamClient: tsClient,
	}
	succeeded = true
	return profilerContext, nil
}

func formatPIDs(pids []int) string {
	values := make([]string, 0, len(pids))
	for _, pid := range pids {
		values = append(values, strconv.Itoa(pid))
	}
	return strings.Join(values, ",")
}

const (
	ScopeAll          = "all"
	ScopePID          = "pid"
	ScopeTGID         = "tgid"
	ScopeCgroup       = "cgroup"
	ScopeProcessGroup = "process-group"
)

// NormalizeScope accepts the original CLI vocabulary while exposing stable
// PID/TGID names in labels and backend queries.
func NormalizeScope(scope string) (string, error) {
	switch strings.TrimSpace(scope) {
	case "", "thread", ScopePID:
		return ScopePID, nil
	case "thread-group", ScopeTGID:
		return ScopeTGID, nil
	case ScopeCgroup:
		return ScopeCgroup, nil
	case ScopeProcessGroup:
		return ScopeProcessGroup, nil
	case ScopeAll:
		return ScopeAll, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// normalizeRequestedScope preserves the original profiler CLI behavior:
// although the default flag text was "thread", an unqualified --pid was
// historically matched against TGID by the native CPU and memory filters.
// An explicitly supplied "thread" still opts into the new per-thread scope.
func normalizeRequestedScope(scope string, explicitlySet bool, pid int) (string, error) {
	normalized, err := NormalizeScope(scope)
	if err != nil {
		return "", err
	}
	if !explicitlySet && pid != 0 && normalized == ScopePID {
		return ScopeTGID, nil
	}
	return normalized, nil
}

func inferImplicitScope(scope string, explicitlySet bool, pid, processGroupID int, cgroupID uint64, containerID string) string {
	if explicitlySet {
		return scope
	}
	switch {
	case cgroupID != 0 || containerID != "":
		return ScopeCgroup
	case processGroupID != 0:
		return ScopeProcessGroup
	case pid != 0:
		// normalizeRequestedScope already preserves the legacy TGID default.
		return scope
	default:
		return ScopeAll
	}
}

// ParseLockTypes normalizes and de-duplicates the requested kernel lock types.
func ParseLockTypes(raw string) ([]string, error) {
	seen := make(map[string]bool)
	result := make([]string, 0, 3)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		switch value {
		case "mutex", "spinlock", "rwlock":
		default:
			return nil, fmt.Errorf("unsupported lock type %q", value)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one lock type is required")
	}
	return result, nil
}

// parseProfileLabels keeps commas and equals signs in label values because the
// CLI disables its slice separator globally.
func parseProfileLabels(flagList []string) (map[string]string, error) {
	labels := make(map[string]string, len(flagList))
	for _, raw := range flagList {
		parts := strings.SplitN(strings.TrimSpace(raw), "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid profile label format %q (expected key=value)", raw)
		}
		name := strings.TrimSpace(strings.TrimLeft(parts[0], "-"))
		if name == "" {
			return nil, fmt.Errorf("invalid profile label format %q (empty name)", raw)
		}
		labels[name] = strings.TrimSpace(parts[1])
	}
	return labels, nil
}

func initToolstreamClient(cliCtx *cli.Context, format output.OutputFormat) (*toolstream.Client, error) {
	if format != output.FormatRemote {
		return nil, nil
	}

	sockPath := cliCtx.String("output-storage")
	if sockPath == "" {
		return nil, fmt.Errorf("--output-storage is required when --output-format=remote")
	}

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "profiler",
		Version:  "1",
	})
	if err != nil {
		return nil, fmt.Errorf("toolstream connect %s: %w", sockPath, err)
	}

	return client, nil
}

func parseCPUIDList(s string) ([]int, error) {
	numCPU := runtime.NumCPU()
	var cpuIDs []int
	seen := make(map[int]bool)

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid cpuid range: %q", part)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range start: %q", rangeParts[0])
			}

			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range end: %q", rangeParts[1])
			}

			if start > end {
				return nil, fmt.Errorf("invalid cpuid range: start %d > end %d", start, end)
			}

			for i := start; i <= end; i++ {
				if i < 0 || i >= numCPU {
					return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", i, numCPU-1)
				}
				if !seen[i] {
					seen[i] = true
					cpuIDs = append(cpuIDs, i)
				}
			}
		} else {
			id, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid: %q", part)
			}

			if id < 0 || id >= numCPU {
				return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", id, numCPU-1)
			}

			if !seen[id] {
				seen[id] = true
				cpuIDs = append(cpuIDs, id)
			}
		}
	}

	if len(cpuIDs) == 0 {
		return nil, fmt.Errorf("cpuid list is empty")
	}

	return cpuIDs, nil
}
