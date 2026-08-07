/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package events

import "strings"

const oomEnvironRedactedValue = "[REDACTED]"

var sensitiveOOMEnvKeywords = []string{
	"PASSWORD",
	"PASSWD",
	"TOKEN",
	"SECRET",
	"CREDENTIAL",
	"API_KEY",
	"ACCESS_KEY",
	"PRIVATE_KEY",
	"COOKIE",
	"SESSION",
	"AUTH",
	"DATABASE_URL",
	"DB_URL",
	"REDIS_URL",
	"MONGO_URI",
	"DATABASE_DSN",
	"CONNECTION_STRING",
	"ENDPOINT",
}

/*
 * redactOOMEnviron preserves useful runtime settings while masking values
 * whose variable names contain a known sensitive keyword. Malformed entries
 * are masked because their variable name cannot be classified safely.
 */
func redactOOMEnviron(environ []string) []string {
	if len(environ) == 0 {
		return environ
	}

	redacted := make([]string, len(environ))
	for i, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" {
			redacted[i] = oomEnvironRedactedValue
			continue
		}

		redacted[i] = entry
		upperName := strings.ToUpper(name)
		for _, keyword := range sensitiveOOMEnvKeywords {
			if strings.Contains(upperName, keyword) {
				redacted[i] = name + "=" + oomEnvironRedactedValue
				break
			}
		}
	}
	return redacted
}
