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

// Check request/file layouts and source-map admission without loading BPF.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
)

func TestValidateRequestPartLayout(t *testing.T) {
	for _, test := range []struct {
		name                         string
		part                         string
		rqDisk, hdStruct, devt, fail bool
		missingDeviceField           bool
	}{
		{name: "legacy", part: "hd_struct", rqDisk: true, hdStruct: true, devt: true},
		{name: "merged", part: "block_device", rqDisk: true},
		{name: "current", part: "block_device"},
		{name: "unused_old_type", part: "block_device", hdStruct: true, devt: true},
		{name: "old_type_without_devt", part: "block_device", rqDisk: true, hdStruct: true},
		{name: "transitional", part: "block_device", rqDisk: true, hdStruct: true, devt: true},
		{name: "legacy_without_rq_disk", part: "hd_struct", hdStruct: true, devt: true},
		{name: "legacy_without_devt", part: "hd_struct", rqDisk: true, hdStruct: true, fail: true},
		{name: "current_without_bd_dev", part: "block_device", missingDeviceField: true, fail: true},
		{name: "unknown_type", part: "other_partition", fail: true},
		{name: "non_pointer", part: "int", fail: true},
		{name: "missing_part", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := requestPartLayoutBTF(t, test.part, test.rqDisk, test.hdStruct, test.devt)
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(data))
			require.NoError(t, err)
			if test.missingDeviceField {
				var device *btf.Struct
				require.NoError(t, spec.TypeByName("block_device", &device))
				device.Members = nil
			}
			blockDevice, err := requestPartIsBlockDeviceFromSpec(spec)
			if test.fail {
				require.ErrorContains(t, err, "unsupported request.part layout")
			} else {
				require.NoError(t, err)
				require.Equal(t, test.part == "block_device", blockDevice)
			}
		})
	}
}

func requestPartLayoutBTF(t *testing.T, partName string, rqDisk, oldType, devt bool) []byte {
	t.Helper()
	integer := &btf.Int{Name: "u32", Size: 4}
	device := &btf.Struct{Name: "device", Size: 4}
	if devt {
		device.Members = []btf.Member{{Name: "devt", Type: integer}}
	}
	hdStruct := &btf.Struct{
		Name: "hd_struct", Size: 4,
		Members: []btf.Member{{Name: "__dev", Type: &btf.Typedef{Name: "device_alias", Type: device}}},
	}
	blockDevice := &btf.Struct{
		Name: "block_device", Size: 4,
		Members: []btf.Member{{Name: "bd_dev", Type: integer}},
	}
	request := &btf.Struct{Name: "request", Size: 16}
	if partName != "" {
		var target btf.Type
		switch partName {
		case "hd_struct":
			target = hdStruct
		case "block_device":
			target = blockDevice
		default:
			target = &btf.Struct{Name: partName}
		}
		var part btf.Type = &btf.Const{Type: &btf.Pointer{Target: target}}
		if partName == "int" {
			part = integer
		}
		request.Members = append(request.Members, btf.Member{Name: "part", Type: part})
	}
	if rqDisk {
		request.Members = append(request.Members, btf.Member{
			Name: "rq_disk", Offset: 64, Type: &btf.Pointer{Target: &btf.Struct{Name: "gendisk"}},
		})
	}
	types := []btf.Type{request}
	if oldType {
		types = append(types, hdStruct)
	}
	builder, err := btf.NewBuilder(types)
	require.NoError(t, err)
	data, err := builder.Marshal(nil, nil)
	require.NoError(t, err)
	return data
}

func TestIOTracingFileReadLayouts(t *testing.T) {
	compiler := strings.Fields(os.Getenv("CC"))
	if len(compiler) == 0 {
		compiler = []string{"cc"}
	}
	bpfDir := os.Getenv("BPF_DIR")
	if bpfDir == "" {
		bpfDir = filepath.Join("..", "..", "bpf")
	}
	arch := map[string]string{"amd64": "x86", "arm64": "arm64"}[runtime.GOARCH]
	require.NotEmpty(t, arch, "native file-read fixture requires amd64 or arm64")
	includes := strings.Fields(os.Getenv("BPF_INCLUDE"))
	includes = append(includes, "-I"+filepath.Join(bpfDir, "include"), "-I"+bpfDir)
	source := filepath.Join("testdata", "test_file_reads.c")

	for _, file := range []string{"3", "2"} {
		for _, inode := range []string{"2", "1"} {
			t.Run("file_"+file+"_inode_"+inode, func(t *testing.T) {
				binary := filepath.Join(t.TempDir(), "file-reads")
				args := []string{
					"-std=gnu11", "-O2", "-Wno-attributes", "-Wno-incompatible-pointer-types",
					"-D__TARGET_ARCH_" + arch, "-DTEST_FILE_PADDING=" + file, "-DTEST_INODE_PADDING=" + inode,
				}
				args = append(args, includes...)
				args = append(args, source, "-o", binary)
				output, err := exec.CommandContext(t.Context(), compiler[0], append(compiler[1:], args...)...).CombinedOutput()
				require.NoError(
					t,
					err,
					"compile native file-read fixture: %s",
					output,
				)
				output, err = exec.CommandContext(t.Context(), binary).CombinedOutput()
				require.NoError(
					t,
					err,
					"run native file-read fixture: %s",
					output,
				)
			})
		}
	}
}
