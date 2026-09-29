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

package executil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"

	"golang.org/x/sys/unix"
)

// MemfdResult separates file data from a command's diagnostic output.
type MemfdResult struct {
	Data   []byte
	Stdout []byte
	Stderr []byte
}

// RunWithMemfd runs one command with an inherited anonymous memory file.
// argsForOutput receives its child-visible path and appends arguments to Spec.Args.
// The command must finish writing before it exits. Empty data is valid; data is
// returned only on success. Standard output and error follow the usual options.
// maxBytes must be positive and limits reading, not the child's file growth.
// The memory file is closed before returning; other inherited files remain owned
// by the caller. Linux 3.17+ and an accessible /proc/self/fd are required.
func RunWithMemfd(
	ctx context.Context,
	spec *Spec,
	argsForOutput func(outputPath string) []string,
	maxBytes int,
	options ...Option,
) (result *MemfdResult, err error) {
	result = &MemfdResult{}
	if ctx == nil {
		return result, errors.New("run with memfd: context must not be nil")
	}
	if spec == nil {
		return result, errors.New("run with memfd: command specification must not be nil")
	}
	if argsForOutput == nil {
		return result, errors.New("run with memfd: output argument builder must not be nil")
	}
	if maxBytes <= 0 {
		return result, errors.New("run with memfd: maximum data bytes must be positive")
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("run with memfd: %w", err)
	}

	fd, err := unix.MemfdCreate("huatuo-exec", unix.MFD_CLOEXEC)
	if err != nil {
		return result, fmt.Errorf("create command output memfd: %w", err)
	}
	file := os.NewFile(uintptr(fd), "huatuo-exec")
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close command output memfd: %w", closeErr))
			result.Data = nil
		}
	}()

	options = append(slices.Clone(options), func(process *Process) {
		outputPath := "/proc/self/fd/" + strconv.Itoa(3+len(process.extraFiles))
		process.extraFiles = append(process.extraFiles, file)
		process.spec.Args = slices.Concat(process.spec.Args, argsForOutput(outputPath))
	})
	process, err := New(*spec, options...)
	if err != nil {
		return result, err
	}
	err = process.Run(ctx)
	result.Stdout = process.Stdout()
	result.Stderr = process.Stderr()
	if err != nil {
		return result, err
	}

	info, err := file.Stat()
	if err != nil {
		return result, fmt.Errorf("stat command output memfd: %w", err)
	}
	if info.Size() > int64(maxBytes) {
		return result, fmt.Errorf("command %q memfd output exceeds %d bytes", spec.Path, maxBytes)
	}

	data := make([]byte, int(info.Size()))
	if _, err := io.ReadFull(io.NewSectionReader(file, 0, info.Size()), data); err != nil {
		return result, fmt.Errorf("read command output memfd: %w", err)
	}
	result.Data = data

	return result, nil
}
