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
	"testing"

	"huatuo-bamai/pkg/processlang"
)

func TestDetectLanguageInfoReadsRetainedGoExecutable(t *testing.T) {
	executable := processlang.OpenExecutable(os.Getpid())
	if executable == nil {
		t.Fatal("failed to open current executable")
	}
	defer executable.Close()

	info := detectLanguageInfo(0, executable)

	if info.Victim != processlang.LanguageGo {
		t.Fatalf("current Go test process detected as %q, want %q",
			info.Victim, processlang.LanguageGo)
	}
}

func TestCompleteLanguageInfoUsesExitCmdline(t *testing.T) {
	oomData := &OOMTracingData{
		Victim: OOMActor{
			Cmdline: "/usr/bin/python3 worker.py",
			Comm:    "java",
		},
		LanguageInfo: &OOMLanguageInfo{
			Victim: processlang.LanguageUnknown,
		},
	}

	completeLanguageInfo(oomData, nil)
	if oomData.LanguageInfo.Victim != processlang.LanguagePython {
		t.Fatalf("exit cmdline language = %q, want %q",
			oomData.LanguageInfo.Victim, processlang.LanguagePython)
	}
}

func TestCompleteLanguageInfoUsesCommWithoutExitCmdline(t *testing.T) {
	oomData := &OOMTracingData{
		Victim: OOMActor{Comm: "java"},
		LanguageInfo: &OOMLanguageInfo{
			Victim: processlang.LanguageUnknown,
		},
	}

	completeLanguageInfo(oomData, nil)
	if oomData.LanguageInfo.Victim != processlang.LanguageJava {
		t.Fatalf("comm language = %q, want %q",
			oomData.LanguageInfo.Victim, processlang.LanguageJava)
	}
}

func TestCompleteLanguageInfoUsesExitGoBuildInfo(t *testing.T) {
	oomData := &OOMTracingData{
		Victim: OOMActor{Comm: "oom-go"},
		LanguageInfo: &OOMLanguageInfo{
			Victim: processlang.LanguageUnknown,
		},
	}

	completeLanguageInfo(oomData, &oomExitContext{goBuildInfo: true})
	if oomData.LanguageInfo.Victim != processlang.LanguageGo {
		t.Fatalf("exit buildinfo language = %q, want %q",
			oomData.LanguageInfo.Victim, processlang.LanguageGo)
	}
}
