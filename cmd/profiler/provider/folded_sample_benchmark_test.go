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

package provider

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func BenchmarkEnqueueFolded(b *testing.B) {
	for _, language := range []profiling.Language{profiling.LanguageJava, profiling.LanguagePython} {
		b.Run(string(language), func(b *testing.B) {
			var text strings.Builder
			for i := range 1000 {
				fmt.Fprintf(&text, "root;main;leaf%d 7\n", i)
			}

			input := profile.SampleOutput{
				PID:    os.Getpid(),
				Output: text.String(),
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := enqueueFolded(
					input,
					[]types.ProfileLabel{types.ProfilePIDLabel(uint32(os.Getpid()))},
					10000000,
					language,
					func(*pipeline.SampleBatch) error { return nil },
				); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
