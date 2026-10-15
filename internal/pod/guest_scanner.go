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

package pod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/procfs"
)

var guestContainerIDPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// GuestScannerOptions describes the Guest-visible process and cgroup trees.
type GuestScannerOptions struct {
	ProcRoot     string
	CgroupRoot   string
	PodName      string
	PodNamespace string
	Interval     time.Duration
}

type guestScanner struct {
	options GuestScannerOptions
}

type guestProcess struct {
	pid         int
	parentPID   int
	startTime   uint64
	containerID string
	cpuPath     string
	memoryPath  string
	legacy      bool
}

// InitGuestManager uses the same cached view and subscriptions as the Host
// manager, without contacting kubelet or the Host container runtime.
func InitGuestManager(options GuestScannerOptions) error {
	if options.PodName == "" || options.PodNamespace == "" {
		return errors.New("guest discovery requires pod name and namespace")
	}
	if options.ProcRoot == "" {
		options.ProcRoot = procfs.DefaultPath()
	}
	if options.CgroupRoot == "" {
		options.CgroupRoot = paths.RootfsDefaultPath
	}
	if options.Interval <= 0 {
		options.Interval = 5 * time.Second
	}
	scanner := &guestScanner{options: options}
	containerManagerMu.Lock()
	defer containerManagerMu.Unlock()
	if containerManager != nil {
		return errors.New("container manager is already running")
	}
	controller := newContainerController(containerView)
	controller.scan = scanner.scan
	controller.resyncInterval = options.Interval
	containerManager = controller
	controller.start()
	return nil
}

func (s *guestScanner) scan(ctx context.Context) (containerSnapshot, error) {
	entries, err := os.ReadDir(s.options.ProcRoot)
	if err != nil {
		return containerSnapshot{}, fmt.Errorf("read guest proc: %w", err)
	}
	if _, err := os.ReadDir(s.options.CgroupRoot); err != nil {
		return containerSnapshot{}, fmt.Errorf("read guest cgroups: %w", err)
	}
	self, err := os.ReadFile(filepath.Join(s.options.ProcRoot, "self", "cgroup"))
	if err != nil {
		return containerSnapshot{}, fmt.Errorf("read guest self cgroup: %w", err)
	}
	selfID, selfPath, _, _ := parseGuestCgroups(string(self))
	if selfID == "" {
		selfID, err = s.selfIDFromMounts()
	}
	// A private cgroup namespace reports the sidecar and its children as /.
	// Those processes have no container ID and are already excluded below.
	if selfID == "" && selfPath != "/" {
		return containerSnapshot{}, fmt.Errorf("resolve guest sidecar identity: %w", err)
	}
	processes := make(map[int]guestProcess)
	var scanErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return containerSnapshot{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		process, err := s.readProcess(pid)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				scanErr = errors.Join(scanErr, err)
			}
			continue
		}
		if process.containerID != "" {
			processes[pid] = process
		}
	}
	grouped := make(map[string][]guestProcess)
	for _, process := range processes {
		if process.containerID != selfID {
			grouped[process.containerID] = append(grouped[process.containerID], process)
		}
	}
	snapshot := containerSnapshot{records: make(map[string]*containerRecord, len(grouped))}
	for id, group := range grouped {
		snapshot.records[id] = nil
		process := representativeGuestProcess(group, processes)
		record, err := s.bind(ctx, &process)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		snapshot.records[id] = record
	}
	// Partial observations must never turn unreadable processes into deletions.
	snapshot.isComplete = scanErr == nil
	return snapshot, scanErr
}

func (s *guestScanner) readProcess(pid int) (guestProcess, error) {
	data, err := os.ReadFile(filepath.Join(s.options.ProcRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return guestProcess{}, err
	}
	id, cpu, memory, legacy := parseGuestCgroups(string(data))
	if id == "" {
		return guestProcess{}, nil
	}
	procFS, err := procfs.NewFS(s.options.ProcRoot)
	if err != nil {
		return guestProcess{}, err
	}
	process, err := procFS.Proc(pid)
	if err != nil {
		return guestProcess{}, err
	}
	stat, err := process.Stat()
	if err != nil {
		return guestProcess{}, err
	}
	return guestProcess{pid: pid, parentPID: stat.PPID, startTime: stat.Starttime, containerID: id, cpuPath: cpu, memoryPath: memory, legacy: legacy}, nil
}

func parseGuestCgroups(content string) (id, cpu, memory string, legacy bool) {
	var unified string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[1] == "" {
			unified = fields[2]
		}
		for _, controller := range strings.Split(fields[1], ",") {
			switch controller {
			case "cpu":
				cpu = fields[2]
			case "memory":
				memory = fields[2]
			}
		}
	}
	legacy = cpu != ""
	if !legacy {
		cpu = unified
		memory = unified
	}
	matches := guestContainerIDPattern.FindAllString(cpu, -1)
	if len(matches) > 0 {
		id = matches[len(matches)-1]
	}
	return id, cpu, memory, legacy
}

func (s *guestScanner) selfIDFromMounts() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.options.ProcRoot, "self", "mountinfo"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// Kata bind-mounts the sidecar rootfs from its own container directory.
		if len(fields) < 5 || fields[4] != "/" {
			continue
		}
		matches := guestContainerIDPattern.FindAllString(fields[3], -1)
		if len(matches) > 0 {
			return matches[len(matches)-1], nil
		}
	}
	return "", errors.New("sidecar container ID missing from cgroup and root mount")
}

func (s *guestScanner) controllerRoot(controller string, legacy bool) string {
	if !legacy {
		return s.options.CgroupRoot
	}
	root := filepath.Join(s.options.CgroupRoot, controller)
	if controller == "cpu" {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			return filepath.Join(s.options.CgroupRoot, "cpu,cpuacct")
		}
	}
	return root
}

func resolveGuestCgroup(ctx context.Context, root, path, id string) (string, error) {
	if strings.HasPrefix(path, "/../") {
		var found string
		err := filepath.WalkDir(root, func(candidate string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() || guestContainerIDPattern.FindString(entry.Name()) != id {
				return nil
			}
			relative, err := filepath.Rel(root, candidate)
			if err != nil {
				return err
			}
			if found != "" {
				return fmt.Errorf("ambiguous guest cgroup for %s", id)
			}
			found = "/" + relative
			return filepath.SkipDir
		})
		if err != nil {
			return "", err
		}
		if found == "" {
			return "", fmt.Errorf("guest cgroup for %s not found under %s: expose the complete Guest cgroup tree", id, root)
		}
		path = found
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return "", fmt.Errorf("invalid guest cgroup %q", path)
	}
	info, err := os.Lstat(filepath.Join(root, path))
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("guest cgroup %q is not a directory", path)
	}
	return path, nil
}

func (s *guestScanner) bind(ctx context.Context, process *guestProcess) (*containerRecord, error) {
	if process.containerID == "" || process.pid <= 0 {
		return nil, errors.New("guest process identity is missing")
	}
	cpu, err := resolveGuestCgroup(ctx, s.controllerRoot("cpu", process.legacy), process.cpuPath, process.containerID)
	if err != nil {
		return nil, err
	}
	var memory string
	var directory os.FileInfo
	if process.memoryPath != "" {
		root := s.controllerRoot("memory", process.legacy)
		memory, err = resolveGuestCgroup(ctx, root, process.memoryPath, process.containerID)
		if err != nil {
			return nil, err
		}
		directory, err = os.Lstat(filepath.Join(root, memory))
		if err != nil {
			return nil, err
		}
	}
	current, err := s.readProcess(process.pid)
	if err != nil {
		return nil, err
	}
	if current.startTime != process.startTime || current.containerID != process.containerID || current.cpuPath != process.cpuPath || current.memoryPath != process.memoryPath {
		return nil, fmt.Errorf("guest process %d changed during discovery", process.pid)
	}
	qos := corev1.PodQOSClass("")
	switch {
	case strings.Contains(cpu, "besteffort"):
		qos = corev1.PodQOSBestEffort
	case strings.Contains(cpu, "burstable"):
		qos = corev1.PodQOSBurstable
	case strings.Contains(cpu, "kubepods"):
		qos = corev1.PodQOSGuaranteed
	}
	level, _ := parseContainerQos(ContainerTypeNormal, &corev1.Pod{Status: corev1.PodStatus{QOSClass: qos}})
	css, _ := parseContainerCSS(process.containerID)
	now := time.Now()
	container := &Container{ID: process.containerID, Name: process.containerID[:12], Hostname: s.options.PodName, Type: ContainerTypeNormal, Qos: level, InitPid: process.pid, CgroupPath: cpu, CgroupCss: css, StartedAt: now, SyncedAt: now, Labels: map[string]any{labelHostNamespace: s.options.PodNamespace}, lifeResources: make(map[string]any)}
	createContainerLifeResources(container)
	return &containerRecord{container: container, ref: ContainerRef{Key: ContainerKey{ID: process.containerID}, InitPID: process.pid, MemoryCgroupPath: memory}, startTime: process.startTime, directory: directory, guest: s}, nil
}

func representativeGuestProcess(group []guestProcess, all map[int]guestProcess) guestProcess {
	representative := group[0]
	rootFound := false
	for _, process := range group {
		parent, exists := all[process.parentPID]
		isRoot := !exists || parent.containerID != process.containerID
		if isRoot && (!rootFound || process.pid < representative.pid) {
			representative = process
			rootFound = true
		}
		if !rootFound && process.pid < representative.pid {
			representative = process
		}
	}
	return representative
}
