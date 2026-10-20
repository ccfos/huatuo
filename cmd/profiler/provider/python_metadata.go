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
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/process"

	hostprocess "github.com/ccfos/huatuo/internal/process"
)

func pythonProcessName(pid int) (string, error) {
	resolvedExe, err := hostprocess.Executable(pid)
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

	return pythonEntryName(cmdline), nil
}
