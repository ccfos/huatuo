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

	"github.com/ccfos/huatuo/internal/utils/cpuutil"
)

// Spec contains stable profiling service parameters.
type Spec struct {
	HostCPUList     string   `json:"host_cpu_list,omitempty"`
	Type            Type     `json:"type"`
	Language        Language `json:"language,omitempty"`
	Mode            Mode     `json:"mode"`
	BinaryMatchPath string   `json:"binary_match_path,omitempty"`
}

// Validate checks the profiling parameter combination against static capabilities.
func (s *Spec) Validate() error {
	if _, err := cpuutil.ParseList(s.HostCPUList); err != nil {
		return fmt.Errorf("invalid host_cpu_list: %w", err)
	}
	if _, err := ParseType(string(s.Type)); err != nil {
		return err
	}
	if s.Type == TypeIRQ {
		if s.Language != LanguageUnknown {
			return fmt.Errorf("language %q is not supported for IRQ profiling", s.Language)
		}
	} else if _, err := ParseLanguage(string(s.Language)); err != nil {
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

// ParseHostCPUList validates collector support separately from API syntax.
// Nodes can report unsupported selections through the existing startup error path.
func (s *Spec) ParseHostCPUList() ([]int, error) {
	ids, err := cpuutil.ParseList(s.HostCPUList)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if s.Type == TypeIRQ {
		if len(ids) > 1 {
			return nil, fmt.Errorf("irqtracing supports only one target CPU; requested %q", s.HostCPUList)
		}
		return ids, nil
	}
	if s.Type == TypeCPU && (s.Language == LanguageC || s.Language == LanguageCPP || s.Language == LanguageGo) {
		return ids, nil
	}
	return nil, fmt.Errorf("CPU targeting is not supported for type %q and language %q", s.Type, s.Language)
}
