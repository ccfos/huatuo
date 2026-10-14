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

package pod

import (
	"encoding/json"
	"testing"
)

func TestContainerTypeString(t *testing.T) {
	tests := []struct {
		name string
		typ  ContainerType
		want string
	}{
		{name: "sidecar", typ: ContainerTypeSidecar, want: "sidecar"},
		{name: "daemonSet", typ: ContainerTypeDaemonSet, want: "daemonSet"},
		{name: "normal", typ: ContainerTypeNormal, want: "normal"},
		{name: "node", typ: ContainerTypeNode, want: "node"},
		{name: "static", typ: ContainerTypeStatic, want: "static"},
		{name: "unknown", typ: ContainerTypeUnknown, want: "unknown"},
		{name: "zero value", typ: 0, want: "unknown"},
		{name: "unmapped bit", typ: 1 << 20, want: "unknown"},
		{name: "combined mask", typ: ContainerTypeSidecar | ContainerTypeNormal, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.typ.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerTypeMarshalJSONUnknown(t *testing.T) {
	// Unknown types must not serialize as an empty string: the metric label
	// and JSON field would be indistinguishable from an unset value.
	payload, err := json.Marshal(ContainerType(1 << 20))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `"unknown"`; got != want {
		t.Fatalf("MarshalJSON() = %s, want %s", got, want)
	}
}
