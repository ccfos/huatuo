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

package profiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
	"github.com/shirou/gopsutil/process"
)

// ParseTree parses the tree data and returns the profile data.
//
// The tree example:
//
// The tree slice example:
//
//	 Raw:
//			sleep;[unknown];_int_free 2385865
//			sleep;_dl_sysdep_start;dl_main;_dl_vdso_vsym 2386677
//			sleep;__mmap;entry_SYSCALL_64_after_hwframe;do_syscall_64;ksys_mmap_pgoff;do_mmap_pgoff 2386677
//
//	Slice:
//		[
//			{["sleep","[unknown]","_int_free"], 2385865},
//			{["sleep","_dl_sysdep_start","dl_main","_dl_vdso_vsym"], 2386677},
//			{["sleep","__mmap","entry_SYSCALL_64_after_hwframe","do_syscall_64","ksys_mmap_pgoff","do_mmap_pgoff"], 2386677},
//		]
//
// The profileType is as follows:
//
//   - CPU sample: process_cpu:cpu:nanoseconds:cpu:nanoseconds
//   - Memory alloc_space: memory:alloc_space:bytes:space:bytes
//   - Memory alloc_objects: memory:alloc_objects:count:space:bytes
func ParseTree(startTime time.Time, profileType string, data []*TreeItem, opt *ParseOption) (*ProfileData, error) {
	profileTypes := strings.Split(profileType, ":")
	if len(profileTypes) != 5 {
		return nil, fmt.Errorf("invalid profile type: %q", profileType)
	}

	tree := ptree.New()

	// foreach
	for _, item := range data {
		tree.InsertStack(item.Stack, item.Value)
	}

	mdata := &ptree.PprofMetadata{
		Type:       profileTypes[1],
		Unit:       profileTypes[2],
		StartTime:  startTime,
		PeriodType: profileTypes[3],
		PeriodUnit: profileTypes[4],
	}

	scale := uint64(1)
	// If the sample rate is set (cpu), scale the tree.
	if opt != nil && opt.SampleRate > 0 {
		scale = uint64(time.Second.Nanoseconds() / opt.SampleRate)
	}
	// otherwise, keep counts unchanged (mem)
	tree.Scale(scale)
	mdata.Period = int64(scale)

	return &ProfileData{
		ProfileType: profileType,
		Profile:     *tree.Pprof(mdata),
	}, nil
}

// ParseCollapsedData parses a collapsed profile (e.g. asprof output) into *ProfileData.
//
// e.g., Java example:
// HotCode.main;HotCode.hotmethod3;java/util/UUID.randomUUID;java/security/SecureRandom.nextBytes;
// read;entry_SYSCALL_64_after_hwframe_[k];do_syscall_64_[k];ksys_read_[k];__check_object_size_[k] 1
func ParseCollapsedData(ctx context.Context, input *ParseInput) (*ProfileData, error) {
	var outputs []SampleOutput
	if err := json.Unmarshal(input.Data, &outputs); err == nil && len(outputs) > 0 {
		for _, out := range outputs {
			if out.PID == 0 || out.Output == "" {
				goto fallback
			}
		}
		return parseMultiProcessData(ctx, input.StartTime, input.ProfileType, input.ProfilerName, outputs, input.Opt,
			func(pid int) ([]byte, error) {
				threadName, err := extractJavaMainClassFromPid(pid)
				if err != nil {
					return nil, err
				}
				return []byte(fmt.Sprintf("process %d:%s", pid, threadName)), nil
			})
	}

fallback:
	return parseCommonData(ctx, input.StartTime, input.ProfileType, input.Data, input.Opt, input.PID, extractJavaMainClassFromPid)
}

// ParseRawData parses a raw profile (e.g. py-spy output) into *ProfileData.
//
// e.g., Python example:
// process 3577332:"python /app/test.py"; __bootstrap (threading.py:784);__bootstrap_inner (threading.py:811);
// run (threading.py:764);worker (more-complex-demo.py:22); level1 (more-complex-demo.py:18);level2 (more-complex-demo.py:15) 10
func ParseRawData(ctx context.Context, input *ParseInput) (*ProfileData, error) {
	var outputs []SampleOutput
	if err := json.Unmarshal(input.Data, &outputs); err == nil && len(outputs) > 0 {
		for _, out := range outputs {
			if out.PID == 0 || out.Output == "" {
				goto fallback
			}
			raw := []byte(out.Output)
			if bytes.Contains(raw, []byte("py-spy>")) {
				lines := bytes.Split(raw, []byte("\n"))
				buf := &bytes.Buffer{}
				for _, line := range lines {
					line = bytes.TrimSpace(line)
					if !bytes.HasPrefix(line, []byte("py-spy>")) {
						buf.Write(line)
						buf.WriteByte('\n')
					}
				}
				out.Output = buf.String()
			}
		}
		return parseMultiProcessData(ctx, input.StartTime, input.ProfileType, input.ProfilerName, outputs, input.Opt, nil)
	}

fallback:
	return parseCommonData(ctx, input.StartTime, input.ProfileType, input.Data, input.Opt, input.PID, extractPythonThreadNameFromPid)
}

// Profiling output for each PID.
type SampleOutput struct {
	PID    int    `json:"pid"`
	Output string `json:"output"`
}

// parseMultiProcessData parses profiles of multiple PIDs from SampleOutput.
func parseMultiProcessData(ctx context.Context, startTime time.Time, profileType, profilerName string, outputs []SampleOutput, opt *ParseOption, getThreadName func(pid int) ([]byte, error)) (*ProfileData, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var tree []*TreeItem

	for _, out := range outputs {
		lines := bytes.Split([]byte(out.Output), []byte("\n"))

		var headerFrame []byte
		if getThreadName != nil {
			var err error
			headerFrame, err = getThreadName(out.PID)
			if err != nil {
				return nil, fmt.Errorf("get header frame for PID %d failed: %w", out.PID, err)
			}
		}

		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			// Find the last space character to separate the call stack from the sample value
			i := bytes.LastIndexByte(line, ' ')
			if i < 0 {
				continue // Skip malformed lines
			}

			stackPart := line[:i]
			valuePart := line[i+1:]

			// Parse the sample count
			value, err := strconv.ParseUint(string(valuePart), 10, 64)
			if err != nil {
				continue
			}
			// Split the stack trace into individual frames
			frames := bytes.Split(stackPart, []byte(";"))

			// Calculate the stack capacity
			// Java: need an additional header frame for process name
			capacity := len(frames)
			if len(headerFrame) > 0 {
				capacity++
			}

			item := &TreeItem{
				Stack: make([][]byte, 0, capacity),
				Value: value,
			}

			if len(headerFrame) > 0 {
				item.Stack = append(item.Stack, headerFrame)
			}

			// Append each frame in order
			for _, frame := range frames {
				frame = bytes.TrimSpace(frame)
				if len(frame) > 0 {
					item.Stack = append(item.Stack, frame)
				}
			}

			tree = append(tree, item)
		}
	}

	// record the timestamp when symbolizing to pprof starts
	SetSymbolizeToPprofTimeStamp(profilerName, time.Now())

	return ParseTree(startTime, profileType, tree, opt)
}

// parseCommonData parses profile of single PID from raw data.
func parseCommonData(ctx context.Context, startTime time.Time, profileType string, b []byte, opt *ParseOption, pid int, getThreadName func(int) (string, error)) (*ProfileData, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	lines := bytes.Split(b, []byte("\n"))
	tree := make([]*TreeItem, 0, len(lines))

	threadName, err := getThreadName(pid)
	if err != nil {
		return nil, fmt.Errorf("extract thread name error: %w", err)
	}

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Find the last space character to separate the call stack from the sample value
		i := bytes.LastIndexByte(line, ' ')
		if i < 0 {
			continue // Skip malformed lines
		}

		stackPart := line[:i]
		valuePart := line[i+1:]

		// Parse the sample count
		value, err := strconv.ParseUint(string(valuePart), 10, 64)
		if err != nil {
			continue // Skip lines with invalid sample values
		}

		// Split the stack trace into individual frames
		frames := bytes.Split(stackPart, []byte(";"))

		item := &TreeItem{
			Stack: make([][]byte, 0, len(frames)+1), // +1 for threadName#pid
			Value: value,
		}

		// Format: threadName#pid
		firstField := fmt.Sprintf("%s#%d", threadName, pid)
		item.Stack = append(item.Stack, []byte(firstField))

		// Append each frame in order
		for _, frame := range frames {
			frame = bytes.TrimSpace(frame)
			if len(frame) > 0 {
				item.Stack = append(item.Stack, frame)
			}
		}

		tree = append(tree, item)
	}

	// Convert the parsed stack data to a ProfileData structure
	return ParseTree(startTime, profileType, tree, opt)
}

func extractJavaMainClassFromPid(pid int) (string, error) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return "", err
	}

	cmdlineSlice, err := p.CmdlineSlice()
	if err != nil {
		return "", err
	}
	if len(cmdlineSlice) == 0 {
		return "", fmt.Errorf("empty cmdline for PID %d", pid)
	}

	// Match java keyword
	if !strings.Contains(cmdlineSlice[0], "java") {
		return "", nil
	}

	cmdlineStr, err := p.Cmdline()
	if err != nil {
		return "", err
	}

	skipNext := false
	var res string
	for i := 1; i < len(cmdlineSlice); i++ {
		arg := cmdlineSlice[i]

		if skipNext {
			skipNext = false
			continue
		}

		if arg == "-cp" || arg == "-classpath" || arg == "--module-path" || arg == "-p" || arg == "--add-opens" {
			skipNext = true
			continue
		}

		if strings.HasPrefix(arg, "-") && arg != "-jar" {
			continue
		}

		if strings.Contains(arg, "=") {
			continue
		}

		if arg == "-jar" && i+1 < len(cmdlineSlice) {
			res = cmdlineSlice[i+1]
		} else if res == "" {
			res = arg
		}

		if res != "" {
			break
		}
	}

	if strings.HasPrefix(res, "-") || res == "" {
		parts := strings.Fields(cmdlineStr)
		if len(parts) > 0 {
			return parts[len(parts)-1], nil
		}
		return "", fmt.Errorf("couldn't get java thread name")
	}

	return res, nil
}

func extractPythonThreadNameFromPid(pid int) (string, error) {
	exePath := fmt.Sprintf("/proc/%d/exe", pid)
	resolvedExe, err := os.Readlink(exePath)
	if err != nil {
		return "", err
	}

	base := filepath.Base(resolvedExe)
	if !strings.HasPrefix(base, "python") {
		return "", fmt.Errorf("process %d exe is %q, not a python process", pid, base)
	}

	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return "", err
	}

	cmdline, err := p.CmdlineSlice()
	if err != nil {
		return "", err
	}
	if len(cmdline) == 0 {
		return "", fmt.Errorf("empty cmdline for PID %d", pid)
	}

	// Extract main script，skip -u -m flags and etc.
	for _, arg := range cmdline[1:] {
		if !strings.HasPrefix(arg, "-") {
			return arg, err
		}
	}

	return "", fmt.Errorf("couldn't get python thread name")
}
