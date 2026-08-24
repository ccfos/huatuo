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

package profiling

import (
	"fmt"
	"slices"
)

// Spec contains stable profiling service parameters.
type Spec struct {
	Type            Type
	Language        Language
	Mode            Mode
	BinaryMatchPath string
}

// Validate checks the profiling parameter combination against static capabilities.
func (s Spec) Validate() error {
	if _, err := ParseType(string(s.Type)); err != nil {
		return err
	}
	if _, err := ParseLanguage(string(s.Language)); err != nil {
		return err
	}
	if _, err := ParseMode(string(s.Mode)); err != nil {
		return err
	}

	capability, ok := capabilityFor(s.Language, s.Type)
	if !ok {
		return fmt.Errorf(
			"profiling type %q is not supported for language %q",
			s.Type,
			s.Language,
		)
	}
	if !slices.Contains(capability.Modes, s.Mode) {
		return fmt.Errorf(
			"profiling mode %q is not supported for type %q and language %q",
			s.Mode,
			s.Type,
			s.Language,
		)
	}
	if s.BinaryMatchPath != "" && !capability.SupportsBinaryMatch {
		return fmt.Errorf(
			"binary match path is not supported for type %q and language %q",
			s.Type,
			s.Language,
		)
	}

	return nil
}
