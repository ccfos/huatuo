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

package xfs

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/prometheus/procfs/xfs"

	"github.com/ccfos/huatuo/internal/procfs"
)

type Stats = xfs.Stats

// FS retains the mount used by both the upstream and metric-specific readers.
type FS struct {
	xfs.FS
	sysPath string
}

// NewDefaultFS returns a new proc FS using runtime-initialized mount points.
func NewDefaultFS() (FS, error) {
	sys := procfs.DefaultPathByType("sys")
	fs, err := xfs.NewFS(
		procfs.DefaultPathByType("proc"),
		sys,
	)
	if err != nil {
		return FS{}, err
	}

	return FS{
		FS:      fs,
		sysPath: sys,
	}, nil
}

// ReadDeviceStats reads /sys/fs/xfs/<device>/stats/stats under the configured sysfs mount.
// Only extent_alloc, ig, push_ail and buf groups are parsed; other groups are ignored.
// Only Name, ExtentAllocation, InodeOperation, PushAil and Buffer are populated.
// Use SysStats when all groups are required.
func (fs FS) ReadDeviceStats() ([]*Stats, error) {
	paths, err := filepath.Glob(filepath.Join(fs.sysPath, "fs/xfs/*/stats/stats"))
	if err != nil {
		return nil, err
	}

	statsList := make([]*Stats, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}

		stats, err := parseDeviceStats(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("parse xfs statistics %s: %w", path, err)
		}

		stats.Name = filepath.Base(filepath.Dir(filepath.Dir(path)))
		statsList = append(statsList, stats)
	}

	return statsList, nil
}

// parseDeviceStats parses extent_alloc, ig, push_ail and buf from a device stats file.
// Other groups are ignored; retained groups use the upstream parser's validation.
func parseDeviceStats(reader io.Reader) (*Stats, error) {
	var collected strings.Builder
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		group := line
		if end := bytes.IndexAny(group, " \t\r\v\f"); end >= 0 {
			group = group[:end]
		}

		// Unused groups may contain sublabels or wider counters. Filter before
		// numeric parsing, retaining the upstream validation for consumed groups.
		switch string(group) {
		case "extent_alloc", "ig", "push_ail", "buf":
			collected.Write(line)
			collected.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return xfs.ParseStats(strings.NewReader(collected.String()))
}

// DefaultPath returns the xfs default path, e.g. "/sys/fs/xfs/"
func DefaultPath() string {
	sys := procfs.DefaultPathByType("sys")

	return filepath.Join(sys, "fs", "xfs")
}

// Path returns a new path with prefix "/sys/fs/xfs/".
func Path(p ...string) string {
	fs := DefaultPath()

	return filepath.Join(append([]string{fs}, p...)...)
}
