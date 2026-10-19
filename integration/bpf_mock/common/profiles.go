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

//go:build integration

package bpfmock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ProfileInput struct {
	Directory    string
	Scenario     string
	PageSize     int64
	ExpectedPID  uint32
	ExpectedComm string
}

func ReadProfileInput(args []string) (ProfileInput, error) {
	var input ProfileInput
	if len(args) != 4 {
		return input, fmt.Errorf("usage: PROFILE_DIR IDENTITY_JSON SCENARIO PAGE_SIZE")
	}
	expectedRaw, err := ReadJSON[struct {
		PID  uint32 `json:"pid"`
		Comm string `json:"comm"`
	}](args[1])
	if err != nil {
		return input, err
	}
	if expectedRaw.PID == 0 || expectedRaw.Comm == "" {
		return input, fmt.Errorf("missing expected task identity")
	}
	pageSize, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil || pageSize <= 0 {
		return input, fmt.Errorf("invalid page size %q", args[3])
	}
	return ProfileInput{
		Directory: args[0], Scenario: args[2], PageSize: pageSize,
		ExpectedPID: expectedRaw.PID, ExpectedComm: expectedRaw.Comm,
	}, nil
}

// VerifyFoldedProfile checks the published native profiler output.
func VerifyFoldedProfile(dir string, expectedPID uint32, expectedComm string, expectedTotalBytes int64, expectedHooks ...string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("profile path is not a directory: %s", dir)
	}
	files, err := filepath.Glob(filepath.Join(dir, "perf_*.folded"))
	if err != nil {
		return err
	}
	var actualTotalBytes int64
	expectedTaskPrefix := fmt.Sprintf("comm=%q;pid=\"%d\";", expectedComm, expectedPID)
	for _, path := range files {
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(contents), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			index := strings.LastIndexByte(line, ' ')
			if index < 0 {
				return fmt.Errorf("invalid folded line: %q", line)
			}
			hasExpectedHook := false
			for _, hook := range expectedHooks {
				hasExpectedHook = hasExpectedHook || strings.Contains(line[:index], hook)
			}
			sampleBytes, err := strconv.ParseInt(line[index+1:], 10, 64)
			if err != nil || sampleBytes <= 0 || !strings.HasPrefix(line[:index], expectedTaskPrefix) || !hasExpectedHook {
				return fmt.Errorf("unexpected folded sample: %q", line)
			}
			actualTotalBytes += sampleBytes
		}
	}
	if actualTotalBytes != expectedTotalBytes {
		return fmt.Errorf("profile captured %d bytes; expected %d", actualTotalBytes, expectedTotalBytes)
	}
	return nil
}
