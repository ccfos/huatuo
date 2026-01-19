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

package executil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

func RunningDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}

	return filepath.Dir(exePath), nil
}

func HostnameByPid(pid uint32) (string, error) {
	var empty string
	fd, err := os.Open(fmt.Sprintf("/proc/%d/ns/uts", pid))
	if err != nil {
		return empty, err
	}
	defer fd.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := unix.Setns(int(fd.Fd()), unix.CLONE_NEWUTS); err != nil {
		return empty, err
	}
	return os.Hostname()
}

func ProcNameByPid(pid uint32) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}

	if len(data) > 128 {
		data = data[:128]
	}

	// Replace null bytes with spaces for readability
	for i := range data {
		if data[i] == 0 {
			data[i] = ' '
		}
	}

	return string(data), nil
}
