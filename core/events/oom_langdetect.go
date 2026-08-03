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

import (
	"os"

	"huatuo-bamai/pkg/processlang"
)

/* OOMLanguageInfo captures the selected victim's detected language. */
type OOMLanguageInfo struct {
	Victim processlang.Language `json:"victim"`
}

func detectLanguageInfo(
	victimPID uint32, executable *os.File,
) *OOMLanguageInfo {
	detected := processlang.DetectLanguageFromExecutable(executable)
	if detected == processlang.LanguageUnknown && victimPID > 0 {
		detected = processlang.DetectLanguage(processlang.ProcessDetails{
			PID: int(victimPID),
		})
	}

	return &OOMLanguageInfo{Victim: detected}
}

/*
 * completeLanguageInfo retries an unknown live result with exit context.
 * A Go buildinfo marker takes precedence over cmdline and comm metadata.
 */
func completeLanguageInfo(
	oomData *OOMTracingData, exitContext *oomExitContext,
) {
	if oomData == nil ||
		(oomData.LanguageInfo != nil &&
			oomData.LanguageInfo.Victim != processlang.LanguageUnknown) {
		return
	}
	if exitContext != nil && exitContext.goBuildInfo {
		oomData.LanguageInfo = &OOMLanguageInfo{Victim: processlang.LanguageGo}
		return
	}

	detected := processlang.DetectLanguage(processlang.ProcessDetails{
		Cmdline: oomData.Victim.Cmdline,
		Comm:    oomData.Victim.Comm,
	})
	oomData.LanguageInfo = &OOMLanguageInfo{Victim: detected}
}
