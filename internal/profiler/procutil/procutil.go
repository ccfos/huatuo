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

package procutil

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/procfs"
)

// CheckExecPath validates whether the actual exec path of pid matches expectedPath.
func CheckExecPath(pid int, expectedPath string) error {
	linkPath := fmt.Sprintf("/proc/%d/exe", pid)
	actualPath, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("readlink %s failed: %w", linkPath, err)
	}
	if actualPath != expectedPath {
		return fmt.Errorf("exec path mismatch: actual=%q, expected=%q", actualPath, expectedPath)
	}
	return nil
}

// CommToString converts a NUL-padded BPF TaskComm byte array to a Go string.
func CommToString(c [bpf.TaskCommLen]byte) string {
	n := bytes.IndexByte(c[:], 0)
	if n == -1 {
		n = len(c)
	}
	return string(c[:n])
}

// ThreadGroupID returns the TGID for pid, which may be a non-leader thread.
func ThreadGroupID(pid int) (int, error) {
	path := procfs.Path(strconv.Itoa(pid), "status")
	status, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open status for pid %d: %w", pid, err)
	}
	defer status.Close()

	tgid, err := parseThreadGroupID(status)
	if err != nil {
		return 0, fmt.Errorf("read TGID for pid %d: %w", pid, err)
	}
	return tgid, nil
}

func parseThreadGroupID(status io.Reader) (int, error) {
	scanner := bufio.NewScanner(status)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "Tgid:" {
			continue
		}
		tgid, err := strconv.Atoi(fields[1])
		if err != nil || tgid < 1 {
			return 0, fmt.Errorf("invalid Tgid value %q", fields[1])
		}
		return tgid, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan status: %w", err)
	}
	return 0, errors.New("Tgid field not found")
}
