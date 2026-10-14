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

	"github.com/ccfos/huatuo/internal/bpf"
)

// attachAndEventPipe attaches shared and available filesystem probes and
// returns a perf reader for iodelay events. Fatal errors close the reader;
// incomplete filesystem groups are retired without stopping other probes.
func attachAndEventPipe(ctx context.Context, b bpf.BPF) (reader bpf.PerfEventReader, disabled map[string]string, err error) {
	reader, err = b.EventPipeByName(ctx, bpfPerfMapName, bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("get event pipe: %w", err)
	}

	// Capture the opened reader before an error return clears the result.
	defer func(reader bpf.PerfEventReader) {
		if err != nil {
			reader.Close()
		}
	}(reader)

	infos, _ := b.Info()

	options, err := buildAttachOptions(infos.ProgramsInfo)
	if err != nil {
		return nil, nil, err
	}

	filesystemErrors, err := attachIOTracingPrograms(b, options)
	if err != nil {
		return nil, nil, err
	}
	for _, filesystem := range []string{"ext4", "xfs"} {
		if failure := filesystemErrors[filesystem]; failure != nil {
			if disabled == nil {
				disabled = make(map[string]string, len(filesystemErrors))
			}
			disabled[filesystem] = failure.Error()
			fmt.Fprintf(os.Stderr, "iotracing: %s file probes disabled; other probes remain active: %v\n", filesystem, failure)
		}
	}

	return reader, disabled, nil
}

func attachIOTracingPrograms(b bpf.BPF, options []bpf.AttachOption) (map[string]error, error) {
	groups := map[string][]bpf.AttachOption{"ext4": nil, "xfs": nil}
	shared := make([]bpf.AttachOption, 0, len(options))
	for _, option := range options {
		filesystem, _, _ := strings.Cut(option.Symbol, "_")
		if _, ok := groups[filesystem]; ok {
			groups[filesystem] = append(groups[filesystem], option)
		} else {
			shared = append(shared, option)
		}
	}
	if err := b.AttachWithOptions(shared); err != nil {
		return nil, fmt.Errorf("attach shared probes: %w", err)
	}
	var failures map[string]error
	for _, filesystem := range []string{"ext4", "xfs"} {
		group := groups[filesystem]
		for index := range group {
			err := b.AttachIndependently(&group[index])
			if err == nil {
				continue
			}
			// Both filesystems share BPF programs. Retire exact targets,
			// not all links on those programs, when a group is incomplete.
			for previous := index - 1; previous >= 0; previous-- {
				if closeErr := b.DetachIndependently(&group[previous]); closeErr != nil {
					return nil, fmt.Errorf("rollback %s after (%w): %w", filesystem, err, closeErr)
				}
			}
			if failures == nil {
				failures = make(map[string]error, 2)
			}
			failures[filesystem] = fmt.Errorf("attach %s: %w", group[index].Symbol, err)
			break
		}
	}
	return failures, nil
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
	if bpf.HasKprobeFunction("rq_qos_issue") {
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
