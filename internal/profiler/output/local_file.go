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

package output

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ccfos/huatuo/internal/log"
)

// localDestination opens lazily so aborted or empty collections leave no result.
type localDestination struct {
	path        string
	format      OutputFormat
	isDirectory bool
	isStdout    bool
	file        *os.File
}

func newLocalDestination(cfg FormatterConfig) (*localDestination, error) {
	d := &localDestination{path: cfg.Destination, format: cfg.Format}
	if d.path == "-" {
		d.isStdout = true
		return d, nil
	}
	if d.path == "" {
		return nil, errors.New("profiling output destination is required")
	}
	info, err := os.Stat(d.path)
	switch {
	case err == nil:
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("profiling output %q must be a file or directory", d.path)
		}
		d.isDirectory = info.IsDir()
	case errors.Is(err, os.ErrNotExist):
		d.isDirectory = strings.HasSuffix(d.path, string(os.PathSeparator))
	default:
		return nil, fmt.Errorf("inspect profiling output %q: %w", d.path, err)
	}
	dir := filepath.Dir(d.path)
	if d.isDirectory {
		dir = d.path
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create profiling output directory %q: %w", dir, err)
	}
	return d, nil
}

func (d *localDestination) open() (io.Writer, error) {
	if d.isStdout {
		return os.Stdout, nil
	}
	if d.file != nil {
		return d.file, nil
	}
	var file *os.File
	var err error
	if d.isDirectory {
		prefix, extension := "perf", ".folded"
		if d.format.IsFlameGraph() {
			prefix, extension = "flamegraph", ".svg"
		} else if d.format == FormatJSON {
			prefix, extension = "profile", ".json"
		}
		file, err = createOutputFile(d.path, prefix, extension)
	} else {
		// Recheck the opened target before truncation: it may have changed since
		// initialization. Nonblocking open avoids hanging on a substituted FIFO.
		file, err = os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|syscall.O_NONBLOCK, 0o666)
		if err == nil {
			info, statErr := file.Stat()
			if statErr != nil {
				err = statErr
			} else if !info.Mode().IsRegular() {
				err = fmt.Errorf("profiling output %q is no longer a regular file", d.path)
			} else {
				err = file.Truncate(0)
			}
			if err != nil {
				err = errors.Join(err, file.Close())
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open profiling output %q: %w", d.path, err)
	}
	d.file = file
	return file, nil
}

func (d *localDestination) writeAndClose(write func(io.Writer) error) error {
	writer, err := d.open()
	if err != nil {
		return err
	}
	if d.isStdout {
		return write(writer)
	}
	file := d.file
	d.file = nil
	if err := writeAndClose(file, write); err != nil {
		return err
	}
	log.WithField("path", file.Name()).Info("profiling data written")
	return nil
}

func (d *localDestination) close() error {
	if d.file == nil {
		return nil
	}
	file := d.file
	d.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("close profiling output %q: %w", file.Name(), err)
	}
	log.WithField("path", file.Name()).Info("profiling output closed")
	return nil
}

func createOutputFile(dir, prefix, ext string) (*os.File, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("generate profiling output filename: %w", err)
	}
	name := fmt.Sprintf("%s_%d_%x%s", prefix, time.Now().Unix(), suffix, ext)
	return os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
}

// writeAndClose preserves delayed filesystem failures as well as write errors.
func writeAndClose(file io.WriteCloser, write func(io.Writer) error) error {
	writeErr := write(file)
	closeErr := file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close profiling output: %w", closeErr)
	}
	return errors.Join(writeErr, closeErr)
}
