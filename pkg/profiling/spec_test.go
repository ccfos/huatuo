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

package profiling

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpecValidateAcceptsEveryCapabilityMode(t *testing.T) {
	t.Parallel()

	for _, capability := range Capabilities() {
		for _, mode := range capability.Modes {
			spec := Spec{
				Type:     capability.Type,
				Language: capability.Language,
				Mode:     mode,
			}
			require.NoError(t, spec.Validate(), "%q/%q/%q", spec.Type, spec.Language, spec.Mode)

			if capability.SupportsBinaryMatch {
				spec.BinaryMatchPath = "/usr/bin/service"
				require.NoError(t, spec.Validate(), "%q/%q/%q", spec.Type, spec.Language, spec.Mode)
			}
		}
	}
}

func TestSpecValidateRejectsInvalidCombination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{
			name:    "unknown type",
			spec:    Spec{Type: "lock", Language: LanguageGo, Mode: ModeOnCPU},
			wantErr: `unsupported profiling type "lock"`,
		},
		{
			name:    "unknown language",
			spec:    Spec{Type: TypeCPU, Language: "rust", Mode: ModeOnCPU},
			wantErr: `unsupported language "rust"`,
		},
		{
			name:    "unknown mode",
			spec:    Spec{Type: TypeCPU, Language: LanguageGo, Mode: "wall_clock"},
			wantErr: `unsupported profiling mode "wall_clock"`,
		},
		{
			name:    "unsupported type",
			spec:    Spec{Type: TypeMemory, Language: LanguagePython, Mode: ModeObjectAlloc},
			wantErr: `profiling type "memory" is not supported for language "python"`,
		},
		{
			name:    "CPU with memory mode",
			spec:    Spec{Type: TypeCPU, Language: LanguageGo, Mode: ModeObjectAlloc},
			wantErr: `profiling mode "object_alloc" is not supported for type "cpu" and language "go"`,
		},
		{
			name:    "Java with off CPU mode",
			spec:    Spec{Type: TypeCPU, Language: LanguageJava, Mode: ModeOffCPU},
			wantErr: `profiling mode "offcpu" is not supported for type "cpu" and language "java"`,
		},
		{
			name: "memory with binary match",
			spec: Spec{
				Type:            TypeMemory,
				Language:        LanguageJava,
				Mode:            ModeObjectAlloc,
				BinaryMatchPath: "/usr/bin/service",
			},
			wantErr: `binary match path is not supported for type "memory" and language "java"`,
		},
		{
			name: "native CPU with binary match",
			spec: Spec{
				Type:            TypeCPU,
				Language:        LanguageGo,
				Mode:            ModeOnCPU,
				BinaryMatchPath: "/usr/bin/service",
			},
			wantErr: `binary match path is not supported for type "cpu" and language "go"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.EqualError(t, tt.spec.Validate(), tt.wantErr)
		})
	}
}
