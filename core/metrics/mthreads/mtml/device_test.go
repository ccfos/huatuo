// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package mtml

import "testing"

func TestSubdeviceCloseRetainsHandleOnFreeFailure(t *testing.T) {
	gpu := &Gpu{handle: 1}
	memory := &Memory{handle: 2}
	vpu := &Vpu{handle: 3}

	tests := []struct {
		name     string
		close    func() error
		handle   func() uintptr
		setFree  func(func(uintptr) Return)
		original func(uintptr) Return
	}{
		{
			name: "gpu", close: gpu.Close, handle: func() uintptr { return gpu.handle },
			setFree:  func(f func(uintptr) Return) { mtmlDeviceFreeGpu = f },
			original: mtmlDeviceFreeGpu,
		},
		{
			name: "memory", close: memory.Close, handle: func() uintptr { return memory.handle },
			setFree:  func(f func(uintptr) Return) { mtmlDeviceFreeMemory = f },
			original: mtmlDeviceFreeMemory,
		},
		{
			name: "vpu", close: vpu.Close, handle: func() uintptr { return vpu.handle },
			setFree:  func(f func(uintptr) Return) { mtmlDeviceFreeVpu = f },
			original: mtmlDeviceFreeVpu,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.setFree(func(uintptr) Return { return ErrorResourceIsBusy })
			defer test.setFree(test.original)

			if err := test.close(); err == nil {
				t.Fatal("Close returned nil after the native free failed")
			}
			if test.handle() == 0 {
				t.Fatal("Close discarded the handle after the native free failed")
			}

			test.setFree(func(uintptr) Return { return Success })
			if err := test.close(); err != nil {
				t.Fatalf("retry Close: %v", err)
			}
			if test.handle() != 0 {
				t.Fatal("Close retained the handle after the native free succeeded")
			}
		})
	}
}
