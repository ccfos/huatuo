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

package main

import (
	"fmt"
	"syscall"
)

// kernelVersion returns the running kernel's major and minor version, or
// (0, 0) if it cannot be determined. (0, 0) resolves to the legacy
// IOCB_DIRECT bit (iocbDirectBit), matching the repo's default 4.18 target,
// so a failure to parse the version falls back to pre-fix behaviour rather
// than mislabelling IO on an unknown kernel.
func kernelVersion() (major, minor int) {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return 0, 0
	}

	release := make([]byte, 0, len(uts.Release))
	for _, c := range uts.Release {
		if c == 0 {
			break
		}
		release = append(release, byte(c))
	}

	return parseRelease(string(release))
}

// parseRelease extracts the leading "major.minor" from a uname release string
// such as "5.10.0-957.el7.x86_64" or "6.18.33.2-microsoft-standard-WSL2". It
// returns (0, 0) when the two leading numeric components cannot be parsed.
func parseRelease(release string) (major, minor int) {
	if _, err := fmt.Sscanf(release, "%d.%d", &major, &minor); err != nil {
		return 0, 0
	}
	return major, minor
}
