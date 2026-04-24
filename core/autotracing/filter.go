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

package autotracing

import (
	"regexp"

	"huatuo-bamai/internal/pod"
)

func ignoreContainer(container *pod.Container, filter filter) bool {
	if len(filter.Include) > 0 {
		included := matchfilterRules(container, filter.Include)
		if !included {
			return true
		}
	}

	if len(filter.Exclude) > 0 {
		excluded := matchfilterRules(container, filter.Exclude)
		if excluded {
			return true
		}
	}

	return false
}

func matchfilterRules(container *pod.Container, rules []filterRule) bool {
	for _, rule := range rules {
		value := getContainerFieldValue(container, rule.Field)
		if value == "" {
			continue
		}

		matched := matchValue(value, rule.Pattern, rule.MatchType)
		if matched {
			return true
		}
	}
	return false
}

func getContainerFieldValue(container *pod.Container, field string) string {
	switch field {
	case "container_host_namespace":
		return container.LabelHostNamespace()
	case "container_hostname":
		return container.Hostname
	case "container_qos":
		return container.Qos.String()
	default:
		return ""
	}
}

func matchValue(value, pattern, matchType string) bool {
	if pattern == "" {
		return false
	}

	switch matchType {
	case "regex":
		re := regexp.MustCompile(pattern)
		return re.MatchString(value)
	case "exact":
		return value == pattern
	default:
		re := regexp.MustCompile(pattern)
		return re.MatchString(value)
	}
}
