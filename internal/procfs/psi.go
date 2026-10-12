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

package procfs

import (
	"bufio"
	"fmt"
	"io"
	"os"

	procfs "github.com/prometheus/procfs"
)

// PSIStatsFromFile parses one pressure-stall-information file, which shares
// the same format in /proc/pressure/<resource> and in cgroup v2
// <cgroup>/<resource>.pressure. Some covers the share of time at least one
// task is stalled, Full the share of time all non-idle tasks are stalled.
func PSIStatsFromFile(path string) (procfs.PSIStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return procfs.PSIStats{}, err
	}
	defer f.Close()

	return parsePSI(f)
}

func parsePSI(r io.Reader) (procfs.PSIStats, error) {
	const lineFormat = "avg10=%f avg60=%f avg300=%f total=%d"

	stats := procfs.PSIStats{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		switch prefix := splitPrefix(line); prefix {
		case "some", "full":
			psi := procfs.PSILine{}
			if _, err := fmt.Sscanf(line, fmt.Sprintf("%s %s", prefix, lineFormat),
				&psi.Avg10, &psi.Avg60, &psi.Avg300, &psi.Total); err != nil {
				return procfs.PSIStats{}, fmt.Errorf("parse PSI line %q: %w", line, err)
			}

			if prefix == "some" {
				stats.Some = &psi
			} else {
				stats.Full = &psi
			}
		default:
			// Ignore unknown prefixes the way the kernel may extend the format.
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return procfs.PSIStats{}, err
	}
	if stats.Some == nil && stats.Full == nil {
		return procfs.PSIStats{}, fmt.Errorf("no pressure data found")
	}

	return stats, nil
}

func splitPrefix(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			return line[:i]
		}
	}
	return line
}
