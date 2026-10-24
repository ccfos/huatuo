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

package main

import (
	"fmt"
	"os"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

func expectedBytes(scenario string, pageSize int64) (int64, error) {
	switch scenario {
	case "mixed":
		return 5 * pageSize, nil
	case "pid-filtered":
		return 0, nil
	}
	return 0, fmt.Errorf("unknown scenario %q", scenario)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input, err := bpfmock.ReadProfileInput(os.Args[1:])
	if err != nil {
		return err
	}
	expectedTotalBytes, err := expectedBytes(input.Scenario, input.PageSize)
	if err != nil {
		return err
	}
	if err := bpfmock.VerifyFoldedProfile(input.Directory, input.ExpectedPID, input.ExpectedComm, expectedTotalBytes, "page_add_new_anon_rmap_mock", "folio_add_new_anon_rmap_mock"); err != nil {
		return err
	}
	fmt.Printf("PASS native_physical_alloc: %s, %d published bytes\n", input.Scenario, expectedTotalBytes)
	return nil
}
