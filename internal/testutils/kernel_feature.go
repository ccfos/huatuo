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

package testutils

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// SkipOnOldKernel skips the current test if the running Linux kernel
// version is lower than the provided minimum version.
//
// minVersion must be provided in "major.minor" format, for example:
//
//	"4.9"
//	"5.10"
//	"6.1"
//
// The feature argument is a short description of the kernel feature
// that requires the specified version.
//
// Warning: this function has no effect on non-Linux platforms.
func SkipOnOldKernel(tb testing.TB, minVersion, feature string) {
	tb.Helper()

	if runtime.GOOS != "linux" {
		tb.Logf("Ignoring version constraint %s for %s on %s", minVersion, feature, runtime.GOOS)
		return
	}

	minMajor, minMinor := mustParseKernelVersion(tb, minVersion)

	kMajor, kMinor := KernelVersion()
	if kMajor == 0 && kMinor == 0 {
		tb.Skip("Cannot determine kernel version")
	}

	if kMajor < minMajor || (kMajor == minMajor && kMinor < minMinor) {
		tb.Skipf("Test requires at least kernel %s (due to missing %s)", minVersion, feature)
	}
}

func mustParseKernelVersion(tb testing.TB, v string) (int, int) {
	tb.Helper()

	majStr, minStr, ok := strings.Cut(v, ".")
	if !ok {
		tb.Fatalf("Invalid kernel version %q (expected major.minor)", v)
	}

	major, err := strconv.Atoi(majStr)
	if err != nil {
		tb.Fatalf("Invalid kernel version %q: %v", v, err)
	}

	minor, err := strconv.Atoi(minStr)
	if err != nil {
		tb.Fatalf("Invalid kernel version %q: %v", v, err)
	}

	return major, minor
}
