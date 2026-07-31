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

import "huatuo-bamai/pkg/processlang"

/* OOMLanguageInfo captures the selected victim's detected language. */
type OOMLanguageInfo struct {
	Victim processlang.Language `json:"victim"`
}

func detectLanguageInfo(victimPID uint32) *OOMLanguageInfo {
	detected := processlang.LanguageUnknown
	if victimPID > 0 {
		detected = processlang.DetectLanguage(processlang.ProcessDetails{
			PID: int(victimPID),
		})
	}

	return &OOMLanguageInfo{Victim: detected}
}

/*
 * completeLanguageInfo retries only an unknown live result with process
 * metadata. Cmdline takes precedence over the kernel command name.
 */
func completeLanguageInfo(oomData *OOMTracingData) {
	if oomData == nil ||
		(oomData.LanguageInfo != nil &&
			oomData.LanguageInfo.Victim != processlang.LanguageUnknown) {
		return
	}

	detected := processlang.DetectLanguage(processlang.ProcessDetails{
		Cmdline: oomData.Victim.Cmdline,
		Comm:    oomData.Victim.Comm,
	})
	oomData.LanguageInfo = &OOMLanguageInfo{Victim: detected}
}
