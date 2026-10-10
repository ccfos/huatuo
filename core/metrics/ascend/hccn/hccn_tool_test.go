// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package hccn

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestHCCNCommandErrorLimitsOutput(t *testing.T) {
	err := hccnCommandError(
		nil,
		bytes.Repeat([]byte("o"), maxHCCNErrorBytes),
		[]byte("stderr tail"),
		errors.New("command failed"),
	)
	if !strings.Contains(err.Error(), "(truncated)") {
		t.Fatalf("hccnCommandError() = %v, want truncation marker", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("o", maxHCCNErrorBytes)) {
		t.Fatalf("hccnCommandError() retained the full stdout error output")
	}
}
