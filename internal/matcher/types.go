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

// Rule holds a field selector and a regex pattern for use in ContainerMatcher.
// Field is the container field name (e.g. FieldTypeContainerHostname);
// Pattern is the regular expression to match against that field's value.
type Rule struct {
	Field   string
	Pattern string
}

// CloneRules returns an independent copy of rules and their elements.
func CloneRules(rules []*Rule) []*Rule {
	if rules == nil {
		return nil
	}

	clone := make([]*Rule, len(rules))
	for i, rule := range rules {
		if rule == nil {
			continue
		}
		clonedRule := *rule
		clone[i] = &clonedRule
	}
	return clone
}
