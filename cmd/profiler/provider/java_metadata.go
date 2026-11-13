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
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/process"
)

// Launcher options that consume the next argument as their value. Options
// whose value names the entry point (-jar, -m/--module) are excluded.
var javaValueOptions = map[string]struct{}{
	"-cp":                    {},
	"-classpath":             {},
	"--class-path":           {},
	"-p":                     {},
	"--module-path":          {},
	"--upgrade-module-path":  {},
	"--add-modules":          {},
	"--limit-modules":        {},
	"--enable-native-access": {},
	"--source":               {},
	"--add-opens":            {},
	"--add-exports":          {},
	"--add-reads":            {},
	"--patch-module":         {},
}

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

		if _, ok := javaValueOptions[arg]; ok {
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
