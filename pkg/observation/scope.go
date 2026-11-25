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

package observation

import (
	"errors"
	"fmt"
)

// Scope identifies the boundary observed by an on-demand operation.
type Scope string

const (
	// ScopeTypeUnknown represents an unset or unsupported observation scope.
	ScopeTypeUnknown Scope = ""
	// ScopeTypeHost observes the entire node.
	ScopeTypeHost Scope = "host"
	// ScopeTypeContainer observes one container on the node.
	ScopeTypeContainer Scope = "container"
)

// ParseScope parses a public observation scope value.
func ParseScope(value string) (Scope, error) {
	scope := Scope(value)
	switch scope {
	case ScopeTypeHost, ScopeTypeContainer:
		return scope, nil
	default:
		return ScopeTypeUnknown, fmt.Errorf("unsupported observation scope %q", value)
	}
}

// ValidateScope checks the relationship between a scope and its container ID.
func ValidateScope(scope Scope, containerID string) error {
	switch scope {
	case ScopeTypeHost:
		if containerID != "" {
			return errors.New("container id must be empty for host scope")
		}
	case ScopeTypeContainer:
		if containerID == "" {
			return errors.New("container id is required for container scope")
		}
	default:
		return fmt.Errorf("unsupported observation scope %q", scope)
	}

	return nil
}
