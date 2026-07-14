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

package java

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	executil "huatuo-bamai/internal/profiler/exec"
)

const (
	asprofFilePollInterval = 100 * time.Millisecond
	stablePollCount        = 2
)

type fileSignature [2]int64

type observedFile struct {
	signature fileSignature
	stable    int
}

type collapsedFile struct {
	path      string
	sequence  uint64
	signature fileSignature
}

type collapsedFileCollector struct {
	pids           []int
	pidsToFilePath map[int]string
	enqueueSample  func(profiler.SampleOutput)
	observedFiles  map[string]observedFile
	retainedFiles  map[string]fileSignature
}

func newCollapsedFileCollector(
	pids []int,
	pidsToFilePath map[int]string,
	enqueueSample func(profiler.SampleOutput),
) *collapsedFileCollector {
	return &collapsedFileCollector{
		pids:           append([]int(nil), pids...),
		pidsToFilePath: pidsToFilePath,
		enqueueSample:  enqueueSample,
		observedFiles:  make(map[string]observedFile),
		retainedFiles:  make(map[string]fileSignature),
	}
}

func (c *collapsedFileCollector) run(ctx context.Context) error {
	ticker := time.NewTicker(asprofFilePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.scanOutputFiles(false); err != nil {
				return err
			}
		}
	}
}

func (c *collapsedFileCollector) scanOutputFiles(force bool) error {
	for _, pid := range c.pids {
		files, err := c.listSortedFiles(pid)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := c.observeOutputFile(pid, file, force); err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *collapsedFileCollector) listSortedFiles(pid int) ([]collapsedFile, error) {
	paths, err := filepath.Glob(c.pidsToFilePath[pid])
	if err != nil {
		return nil, fmt.Errorf("glob output for pid %d: %w", pid, err)
	}

	files := make([]collapsedFile, 0, len(paths))
	for _, path := range paths {
		sequence, err := collapsedFileSequence(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat output for pid %d: %w", pid, err)
		}
		files = append(files, collapsedFile{
			path:      path,
			sequence:  sequence,
			signature: fileSignature{info.Size(), info.ModTime().UnixNano()},
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].sequence < files[j].sequence
	})
	return files, nil
}

func collapsedFileSequence(path string) (uint64, error) {
	base := filepath.Base(path)
	stem, ok := strings.CutSuffix(base, ".collapsed")
	if !ok {
		return 0, fmt.Errorf("collapsed output %q has no sequence", path)
	}
	separator := strings.LastIndexByte(stem, '-')
	if separator < 0 || separator == len(stem)-1 {
		return 0, fmt.Errorf("collapsed output %q has no sequence", path)
	}

	sequenceText := stem[separator+1:]
	sequence, err := strconv.ParseUint(sequenceText, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid sequence %q in collapsed output %q: %w", sequenceText, path, err)
	}
	return sequence, nil
}

func (c *collapsedFileCollector) observeOutputFile(pid int, file collapsedFile, force bool) error {
	if retained, ok := c.retainedFiles[file.path]; ok && retained == file.signature {
		return nil
	}

	observation := c.observedFiles[file.path]
	if observation.signature == file.signature {
		observation.stable++
	} else {
		observation = observedFile{signature: file.signature, stable: 1}
	}
	c.observedFiles[file.path] = observation

	if !force && observation.stable < stablePollCount {
		return nil
	}

	collapsedData, err := os.ReadFile(file.path)
	if err != nil {
		return fmt.Errorf("read output for pid %d: %w", pid, err)
	}
	removeErr := os.Remove(file.path)
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		log.WithError(removeErr).
			WithField("path", file.path).
			WithField("pid", pid).
			Warn("failed to remove consumed async-profiler output")
		c.retainedFiles[file.path] = file.signature
	} else {
		delete(c.retainedFiles, file.path)
	}
	delete(c.observedFiles, file.path)

	c.enqueueSample(profiler.SampleOutput{PID: pid, Output: string(collapsedData)})
	log.WithField("pid", pid).
		WithField("sequence", file.sequence).
		WithField("output_bytes", len(collapsedData)).
		Debug("collected async-profiler output")
	return nil
}

func finishAsprofSampling(
	ctx context.Context,
	opt *AsprofSamplingOption,
	collector *collapsedFileCollector,
) error {
	stopErr := stopAsprofSampling(ctx, opt)
	if err := collector.scanOutputFiles(true); err != nil {
		return errors.Join(stopErr, err)
	}
	return stopErr
}

func stopAsprofSampling(
	ctx context.Context,
	opt *AsprofSamplingOption,
) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asprofCommandTimeout)
	defer cancel()

	activePIDs := opt.activePIDList()
	results := executil.ExecCmds(finalCtx, activePIDs, asprofPath(opt.ToolPath), func(pid int) []string {
		return stopWithOutputArgs(pid, opt.SessionID, opt.OutFilePrefix, opt.outputFileCount)
	})
	finalCtxErr := finalCtx.Err()
	opt.markStopped(results)

	var timeoutErr error
	if finalCtxErr != nil {
		timeoutErr = fmt.Errorf("stop async-profiler after cancellation: %w", finalCtxErr)
	}
	return errors.Join(
		timeoutErr,
		executil.VerifyResults(results),
	)
}
