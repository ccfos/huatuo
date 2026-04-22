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

package patternutil

import (
	"regexp"

	"huatuo-bamai/internal/log"
)

// KnownIssueSearch searches known issue patterns and returns the matched issue name.
func KnownIssueSearch(patternList [][]string, srcPattern, srcMatching1, srcMatching2 string) (issueName string, inKnownList uint64) {
	for _, p := range patternList {
		if len(p) < 2 {
			log.Infof("Invalid configuration, please check the config file!")
			return "", 0
		}

		rePattern := regexp.MustCompile(p[1])
		if rePattern.MatchString(srcPattern) {
			if srcMatching1 != "" && len(p) >= 3 && p[2] != "" {
				re1 := regexp.MustCompile(p[2])
				if re1.MatchString(srcMatching1) {
					return p[0], 1
				}
			}

			if srcMatching2 != "" && len(p) >= 4 && p[3] != "" {
				re2 := regexp.MustCompile(p[3])
				if re2.MatchString(srcMatching2) {
					return p[0], 1
				}
			}

			return p[0], 0
		}
	}

	return "", 0
}
