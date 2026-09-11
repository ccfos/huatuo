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

package collector

import "testing"

func TestRdmaPortTagsIncludesPortLabel(t *testing.T) {
	base := map[string]string{
		"device":    "mlx5_0",
		"nodeguid":  "0000:0000:0000:0000",
		"index":     "0",
		"num_ports": "2",
	}

	tags := rdmaPortTags(base, 1)
	if tags["port"] != "1" {
		t.Fatalf("port label = %q, want 1", tags["port"])
	}
	if tags["device"] != "mlx5_0" {
		t.Fatalf("device label = %q, want mlx5_0", tags["device"])
	}
	if base["port"] != "" {
		t.Fatalf("base labels were mutated: port=%q", base["port"])
	}
}
