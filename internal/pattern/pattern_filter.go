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

package pattern

import (
	"regexp"

	"huatuo-bamai/internal/pod"
)

const (
	FieldTypeContainerHostNamespace = "container_host_namespace"
	FieldTypeContainerHostname      = "container_hostname"
	FieldTypeContainerQos           = "container_qos"
)

type Filter struct {
	Excluded []*Rule
	Included []*Rule
}

func NewFilter(included, excluded string) *Filter {
	f := &Filter{}

	if included != "" {
		f.Included = []*Rule{{Pattern: included}}
	}

	if excluded != "" {
		f.Excluded = []*Rule{{Pattern: excluded}}
	}

	return f
}

func (f *Filter) Ignored(value string) bool {
	anyMatch := func(rules []*Rule) bool {
		for _, r := range rules {
			if r.match(value) {
				return true
			}
		}
		return false
	}

	if len(f.Included) > 0 && !anyMatch(f.Included) {
		return true
	}

	if len(f.Excluded) > 0 && anyMatch(f.Excluded) {
		return true
	}

	return false
}

func (f *Filter) IgnoreContainer(container *pod.Container) bool {
	anyMatch := func(rules []*Rule) bool {
		for _, r := range rules {
			if r.Field != "" && r.matchContainer(container) {
				return true
			}
		}
		return false
	}

	if len(f.Included) > 0 && !anyMatch(f.Included) {
		return true
	}

	if len(f.Excluded) > 0 && anyMatch(f.Excluded) {
		return true
	}

	return false
}

type Rule struct {
	Field   string
	Pattern string
}

func (r *Rule) matchContainer(container *pod.Container) bool {
	return r.match(r.containerFieldValue(container))
}

func (r *Rule) match(value string) bool {
	if value == "" {
		return false
	}

	return regexp.MustCompile(r.Pattern).MatchString(value)
}

func (r *Rule) containerFieldValue(container *pod.Container) string {
	switch r.Field {
	case FieldTypeContainerHostNamespace:
		return container.LabelHostNamespace()
	case FieldTypeContainerHostname:
		return container.Hostname
	case FieldTypeContainerQos:
		return container.Qos.String()
	default:
		return ""
	}
}
