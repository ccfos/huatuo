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

package main

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

func TestBPFFilesystemIOLayout(t *testing.T) {
	t.Parallel()

	var record bpfFilesystemIO

	assert.Equal(t, uintptr(376), unsafe.Sizeof(record))
	assert.Equal(t, uintptr(0), unsafe.Offsetof(record.TGID))
	assert.Equal(t, uintptr(4), unsafe.Offsetof(record.PathInitialized))
	assert.Equal(t, uintptr(8), unsafe.Offsetof(record.DevID))
	assert.Equal(t, uintptr(16), unsafe.Offsetof(record.FsWriteBytes))
	assert.Equal(t, uintptr(64), unsafe.Offsetof(record.Latency))
	assert.Equal(t, uintptr(104), unsafe.Offsetof(record.Comm))
	assert.Equal(t, uintptr(120), unsafe.Offsetof(record.PathSegs))
}

func TestBPFFilesystemIOPathName(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}

		assert.Empty(t, record.PathName())
	})

	t.Run("captured path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}
		copy(record.PathSegs[0][:], "io")
		copy(record.PathSegs[1][:], "huatuo-iotracing.test")
		copy(record.PathSegs[2][:], "/")

		assert.Equal(t, "/huatuo-iotracing.test/io", record.PathName())
	})
}
