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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	promprocfs "github.com/prometheus/procfs"
	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
)

var guestContainerIDPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// GuestScannerOptions describes the Guest-visible process and cgroup trees.
type GuestScannerOptions struct {
	ProcRoot     string
	CgroupRoot   string
	PodName      string
	PodNamespace string
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
	cgroups     *CgroupBinding
}

// NewGuestSource prepares local application discovery without starting a Manager.
func NewGuestSource(options GuestScannerOptions) (Source, error) {
	if options.PodName == "" || options.PodNamespace == "" {
		return nil, errors.New("guest discovery requires pod name and namespace")
	}
	if options.ProcRoot == "" {
		options.ProcRoot = procfs.DefaultPath()
	}
	if options.CgroupRoot == "" {
		options.CgroupRoot = paths.RootfsDefaultPath
	}
	return &guestScanner{options: options}, nil
}

func (*guestScanner) Type() string { return TypeContainer }

func (s *guestScanner) Discover(ctx context.Context) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	entries, err := os.ReadDir(s.options.ProcRoot)
	if err != nil {
		return Discovery{}, fmt.Errorf("read guest proc: %w", err)
	}
	if _, err := os.ReadDir(s.options.CgroupRoot); err != nil {
		return Discovery{}, fmt.Errorf("read guest cgroups: %w", err)
	}
	self, err := os.ReadFile(filepath.Join(s.options.ProcRoot, "self", "cgroup"))
	if err != nil {
		return Discovery{}, fmt.Errorf("read guest self cgroup: %w", err)
	}
	selfID, selfPath, _, _ := parseGuestCgroups(string(self))
	if selfID == "" {
		selfID, err = s.selfIDFromMounts()
	}
	// A private cgroup namespace reports the sidecar and its children as /.
	// Those processes have no container ID and are already excluded below.
	if selfID == "" && selfPath != "/" {
		return Discovery{}, fmt.Errorf("resolve guest sidecar identity: %w", err)
	}
	processes := make(map[int]guestProcess)
	var membershipErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Discovery{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		process, err := s.readProcess(pid)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				membershipErr = errors.Join(membershipErr, err)
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
	discovery := Discovery{Pods: make(map[string]*Pod, len(grouped))}
	for id, group := range grouped {
		if err := ctx.Err(); err != nil {
			return Discovery{}, err
		}
		// Membership is known from the process scan. A nil record keeps the
		// previous instance and prevents deletion while metadata is pending.
		discovery.Pods[id] = nil
		process := representativeGuestProcess(group, processes)
		workload, err := s.resolve(ctx, &process)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Discovery{}, ctxErr
			}
			// Binding is retried on the next scan; it is metadata, not
			// membership, so it must not block deletions for other containers.
			log.WithError(err).WithField("container", id).
				Debug("guest container metadata unavailable")
			continue
		}
		discovery.Pods[id] = workload
	}
	// Only unreadable process entries can hide containers from the membership
	// view. Known containers whose binding failed stay present with a nil
	// record and are retried without freezing the whole view.
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	discovery.Complete = membershipErr == nil
	return discovery, membershipErr
}

func (s *guestScanner) readProcess(pid int) (guestProcess, error) {
	data, err := os.ReadFile(filepath.Join(s.options.ProcRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return guestProcess{}, err
	}
	id, cpu, memory, locations := parseGuestCgroups(string(data))
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
	return guestProcess{
		pid:         pid,
		parentPID:   stat.PPID,
		startTime:   stat.Starttime,
		containerID: id,
		cpuPath:     cpu,
		memoryPath:  memory,
		cgroups:     &locations,
	}, nil
}

func parseGuestCgroups(content string) (id, cpu, memory string, locations CgroupBinding) {
	var unified string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[1] == "" {
			unified = fields[2]
			locations.Unified = unified
		}
		for _, controller := range strings.Split(fields[1], ",") {
			if controller != "" {
				if locations.Controllers == nil {
					locations.Controllers = make(map[string]string)
				}
				locations.Controllers[controller] = fields[2]
			}
			switch controller {
			case "cpu":
				cpu = fields[2]
			case "memory":
				memory = fields[2]
			}
		}
	}
	if cpu == "" {
		cpu = unified
		memory = unified
	}
	matches := guestContainerIDPattern.FindAllString(cpu, -1)
	if len(matches) > 0 {
		id = matches[len(matches)-1]
	}
	return id, cpu, memory, locations
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

func (s *guestScanner) resolve(ctx context.Context, process *guestProcess) (*Pod, error) {
	if process.containerID == "" || process.pid <= 0 {
		return nil, errors.New("guest process identity is missing")
	}
	legacy := process.cgroups.Controllers["cpu"] != ""
	cpu, err := resolveGuestCgroup(ctx, s.controllerRoot("cpu", legacy), process.cpuPath, process.containerID)
	if err != nil {
		return nil, err
	}
	var memory string
	if process.memoryPath != "" {
		root := s.controllerRoot("memory", legacy)
		memory, err = resolveGuestCgroup(ctx, root, process.memoryPath, process.containerID)
		if err != nil {
			return nil, err
		}
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
	level := ParseContainerQos(string(qos))
	locations := *process.cgroups
	locations.Controllers = maps.Clone(locations.Controllers)
	if legacy {
		locations.Controllers["cpu"] = cpu
		if locations.Controllers["cpuacct"] == process.cpuPath {
			locations.Controllers["cpuacct"] = cpu
		}
		if memory != "" {
			locations.Controllers["memory"] = memory
		}
	} else {
		locations.Unified = cpu
	}
	if mounts, err := cgroupMountsAt(s.options.ProcRoot); err == nil {
		for controller, membership := range locations.Controllers {
			if !strings.HasPrefix(membership, "/../") {
				continue
			}
			resolved, err := resolveGuestMembership(ctx, mounts, controller, membership, process.containerID)
			if err == nil {
				locations.Controllers[controller] = resolved
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}
	current, err := s.readProcess(process.pid)
	if err != nil {
		return nil, err
	}
	if !process.sameIdentity(&current) {
		return nil, fmt.Errorf("guest process %d changed during discovery", process.pid)
	}
	return &Pod{
		ID: process.containerID, Type: TypeContainer, Name: process.containerID[:12],
		Cgroups: locations, Process: &HostProcess{PID: process.pid}, StartedAt: time.Now(),
		Labels: map[string]any{LabelHostNamespace: s.options.PodNamespace},
		Container: &ContainerInfo{
			Hostname: s.options.PodName, Type: ContainerTypeNormal, Qos: level,
			Guest: true, CgroupPath: cpu, MemoryCgroupPath: memory,
		},
	}, nil
}

func (p *guestProcess) sameIdentity(other *guestProcess) bool {
	return p.startTime == other.startTime && p.containerID == other.containerID &&
		p.cpuPath == other.cpuPath && p.memoryPath == other.memoryPath &&
		p.cgroups != nil && other.cgroups != nil &&
		p.cgroups.Unified == other.cgroups.Unified && maps.Equal(p.cgroups.Controllers, other.cgroups.Controllers)
}

// A private cgroup namespace may report paths above its visible root. Resolve
// those against actual mounts; a normal absolute membership needs no search.
func resolveGuestMembership(ctx context.Context, mounts []*promprocfs.MountInfo, controller, membership, id string) (string, error) {
	if !strings.HasPrefix(membership, "/../") {
		return membership, nil
	}
	var resultErr error
	for _, mount := range mounts {
		if controller == "" {
			if mount.FSType != "cgroup2" {
				continue
			}
		} else if _, exists := mount.SuperOptions[controller]; mount.FSType != "cgroup" || !exists {
			continue
		}
		resolved, err := resolveGuestCgroup(ctx, mountPathUnescape.Replace(mount.MountPoint), membership, id)
		if err == nil {
			return filepath.Join(mountPathUnescape.Replace(mount.Root), resolved), nil
		}
		resultErr = errors.Join(resultErr, err)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("resolve guest %s membership %q: %w", controller, membership,
		errors.Join(errors.New("no usable cgroup mount"), resultErr))
}

// Validation uses the published process and mounts, not the discovery source.
func validateGuestContainerRef(previous *containerRecord) error {
	if previous.pod.Process == nil {
		return errors.New("guest representative process is unavailable")
	}
	root := previous.pod.Process.procRoot
	scanner := &guestScanner{options: GuestScannerOptions{ProcRoot: root}}
	process, err := scanner.readProcess(previous.ref.InitPID)
	if err != nil {
		return err
	}
	if process.containerID != previous.pod.ID || process.startTime != previous.startTime {
		return fmt.Errorf("guest process %d identity changed", previous.ref.InitPID)
	}
	mounts, err := cgroupMountsAt(root)
	if err != nil {
		return err
	}
	current := &containerRecord{ref: ContainerRef{InitPID: process.pid}, startTime: process.startTime}
	for _, controller := range []string{"cpu", "memory"} {
		membership := process.cgroups.Controllers[controller]
		mountController := controller
		if process.cgroups.Controllers["cpu"] == "" {
			membership, mountController = process.cgroups.Unified, ""
		}
		if membership == "" {
			continue
		}
		membership, err = resolveGuestMembership(context.Background(), mounts, mountController, membership, process.containerID)
		if err != nil {
			return err
		}
		_, info, err := resolveCgroupDirectory(mounts, mountController, membership)
		if err != nil {
			return err
		}
		if controller == "memory" {
			current.ref.MemoryCgroupPath, current.directory = membership, info
		}
	}
	if !previous.sameInstance(current) {
		return fmt.Errorf("guest container %q kernel binding changed", previous.pod.ID)
	}
	after, err := scanner.readProcess(process.pid)
	if err != nil {
		return err
	}
	if !process.sameIdentity(&after) {
		return fmt.Errorf("guest process %d changed during validation", process.pid)
	}
	return nil
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
