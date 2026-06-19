// Copyright 2025 The HuaTuo Authors
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
	"bytes"
	"fmt"
	"os"

	"huatuo-bamai/internal/bpf"
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
