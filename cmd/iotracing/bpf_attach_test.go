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

// Qualify reader ownership when probe setup fails, without loading BPF.
package main

import (
	"context"
	"errors"
	"fmt"
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
	reader   *attachTestReader
	pipeErr  error
	extra    []bpf.ProgramInfo
	failures map[string][]error
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
		if failures := b.failures[option.Symbol]; len(failures) > 0 {
			b.failures[option.Symbol] = failures[1:]
			if failures[0] != nil {
				return failures[0]
			}
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
			reader, err := attachAndEventPipe(context.Background(), b)
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

func TestAttachAndEventPipePreservesXFSFailure(t *testing.T) {
	attachErr := fmt.Errorf("attach XFS probe: %w", syscall.EADDRNOTAVAIL)
	b := &attachTestBPF{reader: new(attachTestReader), failures: map[string][]error{"xfs_file_read_iter": {attachErr}}}
	reader, err := attachAndEventPipe(context.Background(), b)
	require.ErrorIs(t, err, attachErr, "production must retain the original attach failure")
	require.Nil(t, reader)
	require.Equal(t, 1, b.reader.closes)
}
