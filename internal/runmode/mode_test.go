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

package runmode

import "testing"

func TestModeValidation(t *testing.T) {
	for _, mode := range []Mode{"", None, Baremetal, Sandbox} {
		if err := mode.Validate(); err != nil {
			t.Errorf("valid mode %q: %v", mode, err)
		}
	}
	for _, mode := range []Mode{"guest", "SANDBOX", " sandbox"} {
		if err := mode.Validate(); err == nil {
			t.Errorf("invalid mode %q accepted", mode)
		}
	}
}

func TestGuestIdentity(t *testing.T) {
	t.Setenv("BAMAI_POD_NAME", " checkout-api ")
	t.Setenv("BAMAI_POD_NAMESPACE", "payments")
	t.Setenv("BAMAI_NODE_NAME", "node-1")
	identity, err := LoadGuestIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.PodName != "checkout-api" || identity.PodNamespace != "payments" || identity.NodeName != "node-1" {
		t.Errorf("identity=%+v", identity)
	}
	for _, key := range []string{"BAMAI_POD_NAME", "BAMAI_POD_NAMESPACE", "BAMAI_NODE_NAME"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			if _, err := LoadGuestIdentity(); err == nil {
				t.Errorf("missing %s accepted", key)
			}
		})
	}
}
