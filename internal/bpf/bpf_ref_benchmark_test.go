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

package bpf

import "testing"

func BenchmarkReferenceAcquireRelease(b *testing.B) {
	reference := benchmarkReference(b)

	b.ReportAllocs()
	for b.Loop() {
		lease, ok := reference.Acquire()
		if !ok {
			b.Fatal("Acquire() failed")
		}
		lease.Release()
	}
}

func BenchmarkReferenceAcquireReleaseParallel(b *testing.B) {
	reference := benchmarkReference(b)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lease, ok := reference.Acquire()
			if !ok {
				b.Error("Acquire() failed")
				return
			}
			lease.Release()
		}
	})
}

func benchmarkReference(b *testing.B) *Reference {
	b.Helper()

	reference := &Reference{}
	if err := reference.Publish(&defaultBPF{}); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := reference.UnPublish(); err != nil {
			b.Error(err)
		}
	})
	return reference
}
