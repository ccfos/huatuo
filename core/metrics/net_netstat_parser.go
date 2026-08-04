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

package collector

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func parseNetStat(fileName string) (map[string]map[string]string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stats := map[string]map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		nameParts := strings.Fields(scanner.Text())
		if len(nameParts) == 0 || !strings.HasSuffix(nameParts[0], ":") {
			return nil, fmt.Errorf("invalid netstat header in %s: %q", fileName, scanner.Text())
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("missing values for %s in %s", nameParts[0], fileName)
		}
		valueParts := strings.Fields(scanner.Text())
		if len(valueParts) == 0 || valueParts[0] != nameParts[0] {
			return nil, fmt.Errorf("mismatched netstat rows in %s: %q and %q", fileName, nameParts[0], scanner.Text())
		}

		protocol := strings.TrimSuffix(nameParts[0], ":")
		if protocol != "Tcp" && protocol != "TcpExt" {
			continue
		}
		if len(nameParts) != len(valueParts) {
			return nil, fmt.Errorf("mismatch: %s:%s", fileName, protocol)
		}

		stats[protocol] = map[string]string{}
		for i := 1; i < len(nameParts); i++ {
			stats[protocol][nameParts[i]] = valueParts[i]
		}
	}

	return stats, scanner.Err()
}
