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

//go:build integration

package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"
)

func TestIOTracingRequestDevice(t *testing.T) {
	object := os.Getenv("HUATUO_IOTRACING_DEVICE_OBJECT")
	device := os.Getenv("HUATUO_IOTRACING_DEVICE")
	if object == "" || device == "" {
		t.Skip("set HUATUO_IOTRACING_DEVICE_OBJECT and HUATUO_IOTRACING_DEVICE")
	}
	var stat unix.Stat_t
	if err := unix.Stat(device, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK {
		t.Fatalf("%s is not a block device", device)
	}
	major, minor := unix.Major(stat.Rdev), unix.Minor(stat.Rdev)
	if minor == 0 {
		t.Skip("a nonzero device minor is needed to exercise device ID reconstruction")
	}
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture includes iotracing.c to exercise the production helper.
	spec.Programs = map[string]*ebpf.ProgramSpec{
		"test_request_device": spec.Programs["test_request_device"],
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer collection.Close()
	probe, err := link.Kprobe("blk_mq_start_request", collection.Programs["test_request_device"], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()

	// Read directly so a page-cache hit cannot hide a missing block request.
	// The device is never written to.
	if output, err := exec.CommandContext(t.Context(), "dd", "if="+device,
		"of=/dev/null", "bs=4096", "count=16", "iflag=direct", "status=none").CombinedOutput(); err != nil {
		t.Fatalf("read %s: %v\n%s", device, err, output)
	}
	dev := major<<20 | minor
	var result struct {
		Observed    uint32
		BioObserved uint32
		Count       uint64
	}
	if err := collection.Maps["request_devices"].Lookup(dev, &result); err != nil {
		t.Fatalf("no requests attributed to %s (%d:%d): %v", device, major, minor, err)
	}
	if result.Count == 0 {
		t.Fatal("no block requests recorded")
	}
	if result.Observed != dev {
		t.Errorf("request device = %d:%d, want %d:%d",
			result.Observed>>20, result.Observed&((1<<20)-1), major, minor)
	}
	if result.BioObserved != dev {
		t.Errorf("bio device = %d:%d, want %d:%d",
			result.BioObserved>>20, result.BioObserved&((1<<20)-1), major, minor)
	}
	t.Logf("%s (%d:%d): %d requests", device, major, minor, result.Count)
}
