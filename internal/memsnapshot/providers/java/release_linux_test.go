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

package java

import (
	"strings"
	"testing"
)

func TestJavaReleaseBounds(t *testing.T) {
	version, err := parseJavaRelease(t.Context(), strings.NewReader("JAVA_VERSION=\"17.0.12\"\n"))
	if err != nil || version != "17.0.12" {
		t.Fatalf("release: %q, %v", version, err)
	}
	if _, err := parseJavaRelease(t.Context(), strings.NewReader(strings.Repeat("X=1\n", 257))); err == nil {
		t.Fatal("unbounded release metadata")
	}
}
