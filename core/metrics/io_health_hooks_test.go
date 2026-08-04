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

package collector

import (
	"errors"
	"reflect"
	"testing"

	"huatuo-bamai/internal/bpf"

	"golang.org/x/sys/unix"
)

type fakeIOHealthAttachBPF struct {
	bpf.BPF
	attachErrors map[string]error
	detachErr    error
	detachAfter  []int
	detached     []string
	programs     []string
	symbols      []string
}

func (f *fakeIOHealthAttachBPF) AttachWithOptions(options []bpf.AttachOption) error {
	if len(options) != 1 {
		return errors.New("test expects one independent attach option")
	}
	f.programs = append(f.programs, options[0].ProgramName)
	f.symbols = append(f.symbols, options[0].Symbol)
	return f.attachErrors[options[0].ProgramName]
}

func (f *fakeIOHealthAttachBPF) DetachProgram(programName string) error {
	f.detachAfter = append(f.detachAfter, len(f.programs))
	f.detached = append(f.detached, programName)
	return f.detachErr
}

func TestAttachIOHealthHooksDetachesNVMeBootstrapAfterHotplugHooks(t *testing.T) {
	object := &fakeIOHealthAttachBPF{}
	primeAfter := -1
	attached := attachIOHealthHooks(object, func() error {
		primeAfter = len(object.programs)
		return nil
	}, false)

	if want := len(ioHealthHooks) + 2; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	wantPrograms := []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
		"trace_block_rq_error",
		"kretprobe_nvme_change_state",
		"kprobe_nvme_change_state",
	}
	for _, hook := range ioHealthHooks {
		wantPrograms = append(wantPrograms, hook.program)
	}
	if !reflect.DeepEqual(object.programs, wantPrograms) {
		t.Fatalf("attach programs = %v, want %v", object.programs, wantPrograms)
	}
	if got := object.symbols[4]; got != "block/block_rq_error" {
		t.Fatalf("block error symbol = %q, want block/block_rq_error", got)
	}
	if primeAfter != 4 {
		t.Fatalf("NVMe bootstrap ran after %d attaches, want 4", primeAfter)
	}
	if !reflect.DeepEqual(object.detachAfter, []int{4}) {
		t.Fatalf("NVMe bootstrap detach points = %v, want [4]", object.detachAfter)
	}
	if !reflect.DeepEqual(object.detached, []string{"kprobe_nvme_sysfs_show_state"}) {
		t.Fatalf("detached programs = %v", object.detached)
	}
}

func TestAttachIOHealthHooksDegradesOptionalSources(t *testing.T) {
	object := &fakeIOHealthAttachBPF{attachErrors: map[string]error{
		"trace_block_rq_error":         unix.ENOENT,
		"kprobe_nvme_sysfs_show_state": errors.New("mapping unavailable"),
		"kretprobe_nvme_change_state":  errors.New("state unavailable"),
	}}
	primed := false
	attached := attachIOHealthHooks(object, func() error {
		primed = true
		return nil
	}, true)

	if primed {
		t.Fatal("NVMe bootstrap ran without the mapping hook")
	}
	if want := len(ioHealthHooks) + 1; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	if len(object.detachAfter) != 0 {
		t.Fatalf("detached without the NVMe bootstrap hook: %v", object.detachAfter)
	}
	for _, program := range object.programs {
		if program == "kprobe_nvme_change_state" {
			t.Fatalf("state entry attached after return hook failed: %v", object.programs)
		}
	}
	wantPrefix := []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
		"trace_block_rq_error",
		"trace_block_rq_complete_error",
		"kretprobe_nvme_change_state",
	}
	if !reflect.DeepEqual(object.programs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("attach prefix = %v, want %v", object.programs, wantPrefix)
	}
	if got := object.symbols[4:6]; !reflect.DeepEqual(got, []string{
		"block/block_rq_error",
		"block_rq_complete",
	}) {
		t.Fatalf("block error symbols = %v", got)
	}
}

func TestAttachIOHealthHooksSkipsUnsupportedCompleteABI(t *testing.T) {
	object := &fakeIOHealthAttachBPF{attachErrors: map[string]error{
		"trace_block_rq_error": unix.ENOENT,
	}}
	attached := attachIOHealthHooks(object, nil, false)

	if want := len(ioHealthHooks) + 1; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	for _, program := range object.programs {
		if program == "trace_block_rq_complete_error" {
			t.Fatalf("completion fallback attached for unknown ABI: %v", object.programs)
		}
	}
}

func TestIOHealthLegacyBlockCompleteSupported(t *testing.T) {
	for _, test := range []struct {
		release string
		want    bool
	}{
		{release: "4.18.0", want: true},
		{release: "5.10.0-vendor", want: true},
		{release: "5.15.12", want: true},
		{release: "5.16.0"},
		{release: "5.18.0"},
		{release: "invalid"},
	} {
		t.Run(test.release, func(t *testing.T) {
			if got := ioHealthLegacyBlockCompleteSupported(test.release); got != test.want {
				t.Fatalf("legacy block completion support = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAttachIOHealthHooksContinuesAfterNVMePrimeFailure(t *testing.T) {
	object := &fakeIOHealthAttachBPF{}
	attached := attachIOHealthHooks(object, func() error {
		return errors.New("read controller state")
	}, false)

	if want := len(ioHealthHooks) + 2; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	if len(object.programs) != len(ioHealthHooks)+7 {
		t.Fatalf("attach programs after prime failure = %v", object.programs)
	}
	if !reflect.DeepEqual(object.detachAfter, []int{4}) {
		t.Fatalf("NVMe bootstrap detach points = %v, want [4]", object.detachAfter)
	}
}

func TestAttachIOHealthHooksStopsWhenNVMeBootstrapCannotDetach(t *testing.T) {
	object := &fakeIOHealthAttachBPF{detachErr: errors.New("detach failed")}
	attached := attachIOHealthHooks(object, func() error { return nil }, false)

	if attached != 0 {
		t.Fatalf("attached event sources = %d, want 0", attached)
	}
	if !reflect.DeepEqual(object.programs, []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
	}) {
		t.Fatalf("programs after detach failure = %v", object.programs)
	}
}
