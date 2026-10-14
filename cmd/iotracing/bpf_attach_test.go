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

// Qualify probe rollback and reader ownership without loading BPF.
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/bpf"
)

type attachTestReader struct {
	bpf.PerfEventReader
	closes   int
	closeErr error
}

func (r *attachTestReader) Close() error {
	r.closes++
	return r.closeErr
}

type attachTestBPF struct {
	bpf.BPF
	reader    *attachTestReader
	pipeErr   error
	extra     []bpf.ProgramInfo
	failures  map[string][]error
	attached  map[[2]string]bool
	detachErr error

	filesystemCalls int
	detached        []string
}

func (b *attachTestBPF) EventPipeByName(context.Context, string, uint32) (bpf.PerfEventReader, error) {
	if b.pipeErr != nil {
		return nil, b.pipeErr
	}
	return b.reader, nil
}

func (b *attachTestBPF) Info() (*bpf.Info, error) {
	programs := []bpf.ProgramInfo{
		{Name: "test_xfs_read", SectionName: "kprobe/xfs_file_read_iter"},
		{Name: "test_xfs_write", SectionName: "kprobe/xfs_file_write_iter"},
		{Name: "test_xfs_mkwrite", SectionName: "kprobe/xfs_filemap_page_mkwrite"},
		{Name: "test_vfs_read", SectionName: "kprobe/vfs_read"},
	}
	return &bpf.Info{ProgramsInfo: append(programs, b.extra...)}, nil
}

func (b *attachTestBPF) AttachWithOptions(options []bpf.AttachOption) error {
	for _, option := range options {
		if option.KeepAttachedOnError {
			b.filesystemCalls++
		}
		if err := b.attach(&option); err != nil {
			if !option.KeepAttachedOnError {
				clear(b.attached)
			}
			return err
		}
	}
	return nil
}

func (b *attachTestBPF) attach(option *bpf.AttachOption) error {
	if failures := b.failures[option.Symbol]; len(failures) > 0 {
		b.failures[option.Symbol] = failures[1:]
		if failures[0] != nil {
			return failures[0]
		}
	}
	if b.attached == nil {
		b.attached = make(map[[2]string]bool)
	}
	b.attached[[2]string{option.ProgramName, option.Symbol}] = true
	return nil
}

func (b *attachTestBPF) Detach(programNames ...string) error {
	b.detached = append(b.detached, programNames...)
	if b.detachErr != nil {
		return b.detachErr
	}
	for key := range b.attached {
		if len(programNames) == 0 || slices.Contains(programNames, key[0]) {
			delete(b.attached, key)
		}
	}
	return nil
}

func TestAttachAndEventPipeReaderOwnership(t *testing.T) {
	pipeErr := errors.New("event pipe failed")
	attachErr := fmt.Errorf("attach failed: %w", syscall.EIO)
	for _, test := range []struct {
		name               string
		pipeErr, attachErr error
		closeErr           error
		invalidSection     bool
		closes             int
	}{
		{name: "success"},
		{name: "pipe_failure", pipeErr: pipeErr},
		{name: "option_failure", invalidSection: true, closes: 1},
		{name: "attach_failure", attachErr: attachErr, closes: 1},
		{name: "option_close_failure", invalidSection: true, closeErr: syscall.EBADF, closes: 1},
		{name: "attach_close_failure", attachErr: attachErr, closeErr: syscall.EBADF, closes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &attachTestBPF{
				reader: &attachTestReader{closeErr: test.closeErr}, pipeErr: test.pipeErr,
				failures: map[string][]error{"vfs_read": {test.attachErr}},
			}
			if test.invalidSection {
				b.extra = []bpf.ProgramInfo{{Name: "test_invalid", SectionName: "invalid"}}
			}
			reader, _, err := attachAndEventPipe(context.Background(), b)
			switch {
			case test.pipeErr != nil:
				require.ErrorIs(t, err, test.pipeErr)
			case test.attachErr != nil:
				require.ErrorIs(t, err, test.attachErr)
			case test.invalidSection:
				require.ErrorContains(t, err, "invalid section name")
			default:
				require.NoError(t, err)
				require.Same(t, b.reader, reader)
			}
			if err != nil {
				require.Nil(t, reader)
			}
			if test.closeErr != nil {
				require.ErrorIs(t, err, test.closeErr)
			}
			require.Equal(t, test.closes, b.reader.closes)
		})
	}
}

func TestAttachAndEventPipeFilesystemRollback(t *testing.T) {
	attachErr, detachErr := syscall.EPERM, syscall.EIO
	for _, test := range []struct {
		name, symbol, disabled, retained string
		detachErr                        error
		closeErr                         error
		fatal                            bool
	}{
		{name: "xfs_failure", symbol: "xfs_file_write_iter", disabled: "xfs", retained: "ext4"},
		{name: "xfs_last_probe_failure", symbol: "xfs_filemap_page_mkwrite", disabled: "xfs", retained: "ext4"},
		{name: "ext4_failure", symbol: "ext4_file_write_iter", disabled: "ext4", retained: "xfs"},
		{name: "rollback_failure", symbol: "xfs_file_write_iter", detachErr: detachErr, fatal: true},
		{
			name: "rollback_close_failure", symbol: "xfs_file_write_iter",
			detachErr: detachErr, closeErr: syscall.EBADF, fatal: true,
		},
		{name: "shared_failure", symbol: "vfs_read", fatal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &attachTestBPF{
				reader: &attachTestReader{closeErr: test.closeErr}, detachErr: test.detachErr,
				failures: map[string][]error{test.symbol: {attachErr}},
				extra: []bpf.ProgramInfo{
					{Name: "test_ext4_read", SectionName: "kprobe/ext4_file_read_iter"},
					{Name: "test_ext4_write", SectionName: "kprobe/ext4_file_write_iter"},
					{Name: "test_ext4_mkwrite", SectionName: "kprobe/ext4_page_mkwrite"},
				},
			}
			reader, disabled, err := attachAndEventPipe(context.Background(), b)
			if test.fatal {
				require.ErrorIs(t, err, attachErr)
				if test.detachErr != nil {
					require.ErrorIs(t, err, test.detachErr)
				} else {
					require.Zero(t, b.filesystemCalls, "shared failure must prevent filesystem attempts")
					require.Empty(t, b.attached, "shared failure must prevent filesystem attachment")
				}
				if test.closeErr != nil {
					require.ErrorIs(t, err, test.closeErr)
				}
				require.Nil(t, reader)
				require.Nil(t, disabled)
				require.Equal(t, 1, b.reader.closes)
				return
			}
			require.NoError(t, err)
			require.Same(t, b.reader, reader)
			require.Zero(t, b.reader.closes)
			require.Len(t, disabled, 1)
			require.Contains(t, disabled[test.disabled], test.symbol)
			require.Contains(t, disabled[test.disabled], attachErr.Error())
			expectedDetach := []string{"test_" + test.disabled + "_read"}
			if test.symbol == "xfs_filemap_page_mkwrite" {
				expectedDetach = append(expectedDetach, "test_xfs_write")
			}
			require.ElementsMatch(t, expectedDetach, b.detached, "rollback must touch only owned programs")
			for key := range b.attached {
				require.False(t, strings.HasPrefix(key[1], test.disabled+"_"),
					"failed filesystem retained target %v", key)
			}
			require.True(t, b.attached[[2]string{"test_vfs_read", "vfs_read"}])
			require.True(t, b.attached[[2]string{"test_" + test.retained + "_read", test.retained + "_file_read_iter"}])
			require.True(t, b.attached[[2]string{"test_" + test.retained + "_write", test.retained + "_file_write_iter"}])
			mkwrite := "ext4_page_mkwrite"
			if test.retained == "xfs" {
				mkwrite = "xfs_filemap_page_mkwrite"
			}
			require.True(t, b.attached[[2]string{"test_" + test.retained + "_mkwrite", mkwrite}])
			require.NoError(t, reader.Close())
			require.Equal(t, 1, b.reader.closes)
		})
	}
}
