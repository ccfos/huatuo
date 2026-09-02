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

import (
	"fmt"

	"huatuo-bamai/internal/pod"
)

const (
	FieldTypeContainerHostNamespace = "container_host_namespace"
	FieldTypeContainerHostname      = "container_hostname"
	FieldTypeContainerQos           = "container_qos"
)

// ContainerMatcher filters *pod.Container instances using pre-compiled FieldMatcher
// include/exclude semantics. Construct via NewContainerMatcher.
//
// A nil ContainerMatcher always returns true (match-all semantics).
type ContainerMatcher struct {
	*inclusionMatcher[*pod.Container]
}

// NewContainerMatcher compiles include and exclude FieldSpecs into a ContainerMatcher.
// Returns an error if any pattern is invalid.
func NewContainerMatcher(include, exclude []FieldSpec[*pod.Container]) (*ContainerMatcher, error) {
	im, err := newInclusionMatcher(include, exclude)
	if err != nil {
		return nil, err
	}
	return &ContainerMatcher{im}, nil
}

// NewContainerMatcherFromRules is a convenience wrapper that converts Rule slices
// (each Rule carrying a Field name and a Pattern) into a ContainerMatcher.
func NewContainerMatcherFromRules(include, exclude []*Rule) (*ContainerMatcher, error) {
	includeSpecs, err := rulesAsContainerSpecs(include)
	if err != nil {
		return nil, fmt.Errorf("include container rules: %w", err)
	}
	excludeSpecs, err := rulesAsContainerSpecs(exclude)
	if err != nil {
		return nil, fmt.Errorf("exclude container rules: %w", err)
	}

	return NewContainerMatcher(includeSpecs, excludeSpecs)
}

// Match reports whether c passes the filter: present in the include set
// (when one is configured) and absent from the exclude set.
// A nil ContainerMatcher always returns true.
func (cm *ContainerMatcher) Match(c *pod.Container) bool {
	if cm == nil {
		return true
	}
	return cm.inclusionMatcher.match(c)
}

// rulesAsContainerSpecs converts rules with non-empty Field and Pattern into FieldSpecs.
func rulesAsContainerSpecs(rules []*Rule) ([]FieldSpec[*pod.Container], error) {
	specs := make([]FieldSpec[*pod.Container], 0, len(rules))
	for _, r := range rules {
		if r == nil || r.Field == "" || r.Pattern == "" {
			continue
		}
		extract := containerFieldExtractor(r.Field)
		if extract == nil {
			return nil, fmt.Errorf("unsupported field %q", r.Field)
		}
		specs = append(specs, FieldSpec[*pod.Container]{
			Name:    r.Field,
			Pattern: r.Pattern,
			Extract: extract,
		})
	}
	return specs, nil
}

func containerFieldExtractor(field string) func(*pod.Container) string {
	switch field {
	case FieldTypeContainerHostNamespace:
		return func(c *pod.Container) string { return c.LabelHostNamespace() }
	case FieldTypeContainerHostname:
		return func(c *pod.Container) string { return c.Hostname }
	case FieldTypeContainerQos:
		return func(c *pod.Container) string { return c.Qos.String() }
	default:
		return nil
	}
}
