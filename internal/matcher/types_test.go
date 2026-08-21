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

package matcher

import "testing"

func TestCloneRules(t *testing.T) {
	source := []*Rule{
		{Field: FieldTypeContainerHostname, Pattern: "^worker-"},
		nil,
	}

	clone := CloneRules(source)
	if len(clone) != len(source) {
		t.Fatalf("CloneRules() length = %d, want %d", len(clone), len(source))
	}
	if clone[0] == source[0] {
		t.Fatal("CloneRules() shares a rule pointer with source")
	}
	if clone[1] != nil {
		t.Fatal("CloneRules() did not preserve nil rule")
	}
	clone[0].Pattern = "^database-"
	if source[0].Pattern != "^worker-" {
		t.Fatalf("source pattern changed to %q", source[0].Pattern)
	}
}

func TestCloneRulesPreservesNil(t *testing.T) {
	if clone := CloneRules(nil); clone != nil {
		t.Fatalf("CloneRules(nil) = %#v, want nil", clone)
	}
}
