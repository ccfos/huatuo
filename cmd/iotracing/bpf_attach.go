// Copyright 2025, 2026 The HuaTuo Authors
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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"huatuo-bamai/internal/bpf"
)

// attachAndEventPipe attaches all programs found in the BPF object and
// returns a perf reader for iodelay events. The reader is closed if any
// later attach step fails so the caller cannot leak the pipe on error.
func attachAndEventPipe(ctx context.Context, b bpf.BPF) (reader bpf.PerfEventReader, err error) {
	reader, err = b.EventPipeByName(ctx, bpfPerfMapName, 8192)
	if err != nil {
		return nil, fmt.Errorf("get event pipe: %w", err)
	}

	defer func() {
		if err != nil {
			reader.Close()
		}
	}()

	infos, _ := b.Info()

	options, err := buildAttachOptions(infos.ProgramsInfo)
	if err != nil {
		return nil, err
	}

	if err = b.AttachWithOptions(options); err != nil {
		return nil, fmt.Errorf("attach with options: %w", err)
	}

	return reader, nil
}

// buildAttachOptions resolves attach points for every BPF program in the
// object.
//
// rq_qos_issue / rq_qos_done were renamed to __rq_qos_issue /
// __rq_qos_done between kernel 4.19 and 5.0. CentOS 8 (4.18-based) only
// triggers the underscored variants when q->rq_qos is non-nil, which is
// the default for queues built via blk_register_queue → wbt_enable_default
// → wbt_init → rq_qos_add (requires CONFIG_BLK_WBT_MQ=y + blk-mq). We pick
// the symbol that exists on this kernel; with another qos strategy enabled
// either symbol works.
//
// kretprobe of io_schedule must be installed before its kprobe so the
// stack capture in the kprobe sees a populated return-side context.
func buildAttachOptions(programs []bpf.ProgramInfo) ([]bpf.AttachOption, error) {
	requestQosIssue, requestQosDone := "__rq_qos_issue", "__rq_qos_done"
	if hasKprobeFunction("rq_qos_issue") {
		requestQosIssue, requestQosDone = "rq_qos_issue", "rq_qos_done"
	}

	var options []bpf.AttachOption

	for _, p := range programs {
		if isAnyfsProgram(p.Name) {
			continue
		}

		switch p.Name {
		case "bpf_rq_qos_issue":
			options = append(options, bpf.AttachOption{
				ProgramName: p.Name,
				Symbol:      requestQosIssue,
			})
		case "bpf_rq_qos_done":
			options = append(options, bpf.AttachOption{
				ProgramName: p.Name,
				Symbol:      requestQosDone,
			})
		default:
			secParts := strings.Split(p.SectionName, "/")
			if len(secParts) != 2 {
				return nil, fmt.Errorf("invalid section name: %s", p.SectionName)
			}

			opt := bpf.AttachOption{ProgramName: p.Name, Symbol: secParts[1]}
			if secParts[0] == "kretprobe" {
				options = append([]bpf.AttachOption{opt}, options...)
			} else {
				options = append(options, opt)
			}
		}
	}

	options = append(options, anyfsAttachOptions()...)

	return options, nil
}

// hasKprobeFunction returns whether the given symbol is reported as
// attachable in the kernel's available_filter_functions list.
func hasKprobeFunction(name string) bool {
	file, err := os.Open("/sys/kernel/debug/tracing/available_filter_functions")
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.Fields(line)[0] == name {
			return true
		}
	}

	return false
}

// isAnyfsProgram is true for filesystem-agnostic stub programs that
// buildAttachOptions skips: their attach points are decided later by
// anyfsAttachOptions based on which filesystems the host actually has.
func isAnyfsProgram(name string) bool {
	return strings.HasPrefix(name, "bpf_anyfs")
}

// hasFilesystem returns whether /proc/filesystems advertises name as a
// supported filesystem on this host.
func hasFilesystem(name string) bool {
	file, err := os.Open("/proc/filesystems")
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 && fields[len(fields)-1] == name {
			return true
		}
	}

	return false
}

// anyfsAttachOptions enumerates the bpf_anyfs_* attach points for each
// filesystem present on the host. Probing only what /proc/filesystems
// advertises avoids attach errors on hosts that ship without ext4 or xfs.
func anyfsAttachOptions() []bpf.AttachOption {
	var opts []bpf.AttachOption

	if hasFilesystem("ext4") {
		opts = append(
			opts,
			bpf.AttachOption{ProgramName: "bpf_anyfs_file_read_iter", Symbol: "ext4_file_read_iter"},
			bpf.AttachOption{ProgramName: "bpf_anyfs_file_write_iter", Symbol: "ext4_file_write_iter"},
			bpf.AttachOption{ProgramName: "bpf_anyfs_filemap_page_mkwrite", Symbol: "ext4_page_mkwrite"},
		)
	}

	if hasFilesystem("xfs") {
		opts = append(
			opts,
			bpf.AttachOption{ProgramName: "bpf_anyfs_file_read_iter", Symbol: "xfs_file_read_iter"},
			bpf.AttachOption{ProgramName: "bpf_anyfs_file_write_iter", Symbol: "xfs_file_write_iter"},
			bpf.AttachOption{ProgramName: "bpf_anyfs_filemap_page_mkwrite", Symbol: "xfs_filemap_page_mkwrite"},
		)
	}

	return opts
}
