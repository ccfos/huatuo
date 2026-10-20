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

import (
	"fmt"
	"os"
	"strings"
)

type Mode string

const (
	None      Mode = "none"
	Baremetal Mode = "baremetal"
	Sandbox   Mode = "sandbox"
)

func (m Mode) Validate() error {
	switch m {
	case "", None, Baremetal, Sandbox:
		return nil
	default:
		return fmt.Errorf("invalid mode %q: expected none, baremetal or sandbox", m)
	}
}

// GuestIdentity contains local discovery labels and the physical node name.
// Host ingestion must resolve its own trusted identity for received documents.
type GuestIdentity struct {
	PodName      string
	PodNamespace string
	NodeName     string
}

func LoadGuestIdentity() (GuestIdentity, error) {
	identity := GuestIdentity{
		PodName:      strings.TrimSpace(os.Getenv("BAMAI_POD_NAME")),
		PodNamespace: strings.TrimSpace(os.Getenv("BAMAI_POD_NAMESPACE")),
		NodeName:     strings.TrimSpace(os.Getenv("BAMAI_NODE_NAME")),
	}
	for _, field := range []struct{ name, value string }{
		{"BAMAI_POD_NAME", identity.PodName},
		{"BAMAI_POD_NAMESPACE", identity.PodNamespace},
		{"BAMAI_NODE_NAME", identity.NodeName},
	} {
		if field.value == "" {
			return GuestIdentity{}, fmt.Errorf("sandbox identity: %s is required", field.name)
		}
	}
	return identity, nil
}
