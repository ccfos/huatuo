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

package autotracing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
)

const (
	maxCgroupDirectories = 8192
	maxWatchedCgroups    = 4096
	maxCgroupScanEntries = 65536
	maxCgroupDepth       = 64
)

var containerCgroupIDRegexp = regexp.MustCompile(
	`^(?:([0-9a-f]{64})|(?:cri-containerd-|docker-|crio-)([0-9a-f]{64})\.scope)$`,
)

var errCgroupWatchLimit = errors.New("memory threshold snapshot cgroup discovery/watch safety limit reached")

func knownContainerCgroupPath(id string) (string, error) {
	container, err := pod.ContainerByID(id)
	if err != nil {
		return "", err
	}
	if container == nil {
		return "", fmt.Errorf("container %q is no longer known", id)
	}
	return memcgPathForPID(container.InitPid, cgroups.CgroupMode())
}

func memcgPathForPID(initPID int, mode cgroups.Mode) (string, error) {
	paths, err := cgroups.PathsForPID(initPID)
	if err != nil {
		return "", err
	}
	// CPU and memory controllers can use different parents on v1.
	path := paths.Controllers["memory"]
	if mode == cgroups.Unified {
		path = paths.Unified
	}
	if path == "" {
		return "", fmt.Errorf("container init pid %d has no memory cgroup path", initPID)
	}
	return path, nil
}

func validateContainerCgroup(id, path string, lookup func(string) (string, error)) error {
	if lookup == nil {
		return errors.New("memory threshold snapshot container path lookup is unavailable")
	}
	known, err := lookup(id)
	if err != nil {
		return err
	}
	if known == "" || !filepath.IsAbs(path) || !filepath.IsAbs(known) ||
		filepath.Clean(known) != filepath.Clean(path) {
		return fmt.Errorf("container %q does not match cgroup %q", id, path)
	}
	return nil
}

func (w *pressureWatcher) requestRecovery() {
	w.recoveryRequested = true
	if w.recoveryDue.IsZero() {
		w.recoveryDue = time.Now().Add(time.Second)
		log.Info("memory threshold snapshot cgroup recovery scheduled: lifecycle loss or registration not ready")
	}
}

func (w *pressureWatcher) recoverWatches(ctx context.Context, events chan<- memoryPressureEvent) error {
	w.recoveryRequested = false
	w.recoveryAttempts++
	started := time.Now()
	err := w.refreshFromCgroupTree(ctx, events)
	log.WithField("attempt", w.recoveryAttempts).
		WithField("watches", len(w.cgroups)).
		WithField("elapsed_ms", time.Since(started).Milliseconds()).
		WithField("retry", w.recoveryRequested).
		WithError(err).
		Info("memory threshold snapshot cgroup recovery finished")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if isResourceExhaustion(err) {
		return err
	}
	if err != nil || w.recoveryRequested {
		if w.recoveryAttempts < 3 {
			w.recoveryDue = time.Now().Add(time.Second)
			return nil
		}
		log.WithError(err).
			Warn("memory threshold snapshot cgroup recovery exhausted; some containers may remain unmonitored")
	}
	w.recoveryDue = time.Time{}
	w.recoveryRequested = false
	w.recoveryAttempts = 0
	return nil
}

func (w *pressureWatcher) refreshFromCgroupTree(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	desired := make(map[string]string)
	err := w.walkCgroupTree(ctx, w.root, func(containerID, cgroupPath string) error {
		if previous, exists := desired[containerID]; exists && previous != cgroupPath {
			// Do not let directory enumeration order choose an identity.
			desired[containerID] = ""
		} else {
			desired[containerID] = cgroupPath
		}
		return nil
	})
	incomplete := errors.Is(err, errCgroupWatchLimit)
	if err != nil && !incomplete {
		return fmt.Errorf("discover memory threshold snapshot cgroups: %w", err)
	}
	if incomplete {
		w.reportWatchLimit()
	}
	addedPaths, err := w.reconcile(desired, !incomplete)
	if errors.Is(err, errCgroupWatchLimit) {
		w.reportWatchLimit()
	} else if err != nil {
		return err
	}
	if w.mode == cgroups.Unified {
		return nil
	}
	for _, cgroupPath := range addedPaths {
		if err := w.emitPressure(ctx, events, cgroupPath); err != nil {
			return err
		}
	}
	return nil
}

func (w *pressureWatcher) reportWatchLimit() {
	if !w.limitReported {
		log.WithError(errCgroupWatchLimit).
			Warn("additional cgroups may not be monitored")
		w.limitReported = true
	}
}

func (w *pressureWatcher) walkCgroupTree(ctx context.Context, start string,
	visitContainer func(containerID, cgroupPath string) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(start)
	if err != nil {
		return err
	}
	if !info.IsDir() || !pathWithin(w.root, start) {
		return fmt.Errorf("invalid cgroup discovery root %q", start)
	}
	relative, err := filepath.Rel(w.root, start)
	if err != nil {
		return err
	}
	depth := 0
	if relative != "." {
		depth = strings.Count(relative, string(filepath.Separator)) + 1
	}
	if depth > maxCgroupDepth {
		return errCgroupWatchLimit
	}
	type pendingDirectory struct {
		path  string
		depth int
	}
	queue := []pendingDirectory{{path: start, depth: depth}}
	entriesRead, containers := 0, 0
	for index := 0; index < len(queue); index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := queue[index]
		if id := parseContainerID(filepath.Base(current.path)); id != "" {
			containers++
			if containers > maxWatchedCgroups {
				return errCgroupWatchLimit
			}
			path, err := relativeCgroupPath(w.root, current.path)
			if err != nil {
				return err
			}
			if err := visitContainer(id, path); err != nil {
				return err
			}
			continue
		}
		// Read bounded batches, rather than WalkDir's whole-directory sort.
		fd, err := unix.Open(current.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		directory := os.NewFile(uintptr(fd), current.path)
		scanErr := func() error {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				entries, err := directory.ReadDir(128)
				entriesRead += len(entries)
				if entriesRead > maxCgroupScanEntries {
					return errCgroupWatchLimit
				}
				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					if len(queue) >= maxCgroupDirectories || current.depth >= maxCgroupDepth {
						return errCgroupWatchLimit
					}
					queue = append(queue, pendingDirectory{
						path: filepath.Join(current.path, entry.Name()), depth: current.depth + 1,
					})
				}
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		}()
		_ = directory.Close()
		if scanErr != nil {
			return scanErr
		}
	}
	return ctx.Err()
}

func parseContainerID(name string) string {
	match := containerCgroupIDRegexp.FindStringSubmatch(name)
	if len(match) < 2 {
		return ""
	}
	if match[1] != "" {
		return match[1]
	}
	return match[2]
}

func relativeCgroupPath(root, fullPath string) (string, error) {
	relativePath, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(relativePath), nil
}

func (w *pressureWatcher) reconcile(desired map[string]string, complete bool) ([]string, error) {
	var addedPaths []string
	// Unseen entries are not deleted when discovery did not finish.
	if complete {
		for cgroupPath, entry := range w.cgroups {
			if desiredPath, ok := desired[entry.containerID]; !ok || desiredPath != cgroupPath {
				w.removeCgroup(cgroupPath)
			}
		}
	}
	for containerID, cgroupPath := range desired {
		if containerID == "" || cgroupPath == "" {
			continue
		}
		added, err := w.watchContainer(containerID, cgroupPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				w.requestRecovery()
				continue
			}
			if !complete && errors.Is(err, errCgroupWatchLimit) {
				return addedPaths, err
			}
			return nil, err
		}
		if added {
			addedPaths = append(addedPaths, cgroupPath)
		}
	}
	return addedPaths, nil
}

func (w *pressureWatcher) watchContainer(containerID,
	cgroupPath string,
) (bool, error) {
	if oldPath, ok := w.cgroupPathForContainer(containerID); ok {
		if oldPath == cgroupPath {
			info, err := os.Lstat(w.memcgDir(cgroupPath))
			if err != nil {
				return false, err
			}
			old := w.cgroups[oldPath]
			if old.identity != nil && os.SameFile(old.identity, info) {
				return false, nil
			}
			w.removeCgroup(oldPath)
		} else {
			if _, err := os.Stat(w.memcgDir(oldPath)); !errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			w.removeCgroup(oldPath)
		}
	}
	if entry, ok := w.cgroups[cgroupPath]; ok {
		entry.containerID = containerID
		return false, nil
	}
	return true, w.addCgroup(containerID, cgroupPath)
}

func (w *pressureWatcher) cgroupPathForContainer(containerID string) (string, bool) {
	for cgroupPath, entry := range w.cgroups {
		if entry.containerID == containerID {
			return cgroupPath, true
		}
	}
	return "", false
}

func (w *pressureWatcher) handleCgroupChanges(ctx context.Context,
	events chan<- memoryPressureEvent,
) error {
	// Bound each batch so lifecycle churn cannot starve pressure notifications.
	for i := 0; i < 256; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case change := <-w.changes:
			if err := w.handleCgroupChange(ctx, events, change); err != nil {
				return err
			}
		default:
			return nil
		}
	}
	signalEventFD(w.controlFD)
	return nil
}

func (w *pressureWatcher) handleCgroupChange(ctx context.Context,
	events chan<- memoryPressureEvent, change pod.MemoryCgroupChange,
) error {
	id := change.ContainerID
	if pod.ValidateContainerID(id) != nil {
		return nil
	}
	cgroupPath, known := w.cgroupPathForContainer(id)
	if change.Removed && !known {
		return nil
	}
	if !change.Removed {
		var err error
		cgroupPath, err = w.containerPath(id)
		if err != nil {
			if w.recoveryDue.IsZero() {
				log.WithField("container", id).
					WithError(err).
					Info("memory threshold snapshot cgroup path lookup deferred")
			}
			w.requestRecovery()
			return nil
		}
	}
	if !strings.HasPrefix(cgroupPath, "/") || filepath.Clean(cgroupPath) != cgroupPath ||
		parseContainerID(filepath.Base(cgroupPath)) != id {
		w.requestRecovery()
		return nil
	}
	// Startup discovery stops at container boundaries; apply the same rule here.
	for parent := filepath.Dir(cgroupPath); parent != "/"; parent = filepath.Dir(parent) {
		if parseContainerID(filepath.Base(parent)) != "" {
			return nil
		}
	}
	// Runtime resolution is authoritative even while the old directory exists.
	if oldPath, ok := w.cgroupPathForContainer(id); !change.Removed && ok && oldPath != cgroupPath {
		if err := w.addCgroup(id, cgroupPath); err != nil {
			if isResourceExhaustion(err) {
				return err
			}
			w.requestRecovery()
			if errors.Is(err, errCgroupWatchLimit) {
				w.reportWatchLimit()
				return nil
			}
			return nil
		}
		w.removeCgroup(oldPath)
		if w.mode != cgroups.Unified {
			return w.emitPressure(ctx, events, cgroupPath)
		}
		return nil
	}
	err := w.updateCgroupPath(ctx, events, id, cgroupPath)
	if errors.Is(err, os.ErrNotExist) {
		w.removeCgroup(cgroupPath)
		if !change.Removed {
			w.requestRecovery()
		}
		return nil
	}
	if errors.Is(err, errCgroupWatchLimit) {
		w.reportWatchLimit()
		return nil
	}
	return err
}

func (w *pressureWatcher) updateCgroupPath(ctx context.Context,
	events chan<- memoryPressureEvent, id, cgroupPath string,
) error {
	info, err := os.Lstat(w.memcgDir(cgroupPath))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	// Consult this path's current inode: queued deletes/creates can arrive after
	// a replacement, including notifications from different per-CPU buffers.
	if old := w.cgroups[cgroupPath]; old != nil {
		if old.identity != nil && os.SameFile(old.identity, info) {
			return nil
		}
		w.removeCgroup(cgroupPath)
	}
	added, err := w.watchContainer(id, cgroupPath)
	if err != nil {
		return err
	}
	if added && w.mode != cgroups.Unified {
		return w.emitPressure(ctx, events, cgroupPath)
	}
	return nil
}

func pathWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func memoryCgroupRoot(mode cgroups.Mode) (string, error) {
	var root string
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		root = cgroups.RootFsFilePath(subsystem.SubsystemMemory)
	case cgroups.Unified:
		root = cgroups.RootfsDefaultPath()
	default:
		return "", fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve memory cgroup root %q: %w", root, err)
	}
	return realRoot, nil
}

func (w *pressureWatcher) memcgDir(cgroupPath string) string {
	cleanPath := strings.TrimPrefix(filepath.Clean("/"+cgroupPath), "/")
	return filepath.Join(w.root, cleanPath)
}
