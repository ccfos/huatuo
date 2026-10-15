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

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/process"

	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func javaProcessName(pid int) (string, error) {
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

// javaProcessNames resolves only records that require a process frame.
func javaProcessNames(ctx context.Context, outputs []profile.SampleOutput, fallbackPID int) (map[int]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, output := range outputs {
		if output.PID == 0 {
			name, err := javaProcessName(fallbackPID)
			if err != nil {
				return nil, fmt.Errorf("extract thread name error: %w", err)
			}
			return map[int]string{fallbackPID: name}, nil
		}
	}
	names := make(map[int]string, len(outputs))
	for _, output := range outputs {
		if output.Output == "" {
			continue
		}
		name, err := javaProcessName(output.PID)
		if err != nil {
			return nil, fmt.Errorf("get header frame for PID %d failed: %w", output.PID, err)
		}
		names[output.PID] = name
	}
	return names, nil
}
