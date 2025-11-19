// Copyright 2025 The HuaTuo Authors
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
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"huatuo-bamai/internal/log"
)

var (
	// all containers, map: ContainerID -> *Container
	containers = map[string]*Container{}

	// updated
	lastUpdatedAt = time.Now()
	updatedStep   = 5 * time.Second
	updatedLock   sync.Mutex
)

// Container object
type Container struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Hostname          string            `json:"hostname"`
	Type              ContainerType     `json:"type"`
	Qos               ContainerQos      `json:"qos"`
	IPAddress         string            `json:"ip_address"`
	NetNamespaceInode uint64            `json:"net_namespace_inode"`
	InitPid           int               `json:"init_pid"` // the pid-1 of container
	CgroupSuffix      string            `json:"cgroup_suffix"`
	CSS               map[string]uint64 `json:"css"`        // map: Name -> Address
	StartedAt         time.Time         `json:"started_at"` // started time
	SyncedAt          time.Time         `json:"synced_at"`  // synced time
	lifeResouces      map[string]any
	Labels            map[string]any `json:"labels"` // custom labels
}

func (c *Container) String() string {
	return fmt.Sprintf("%s:%s/%s/%s:%s/%s", c.ID, c.Hostname, c.Name, c.Type, c.Qos, c.IPAddress)
}

// LifeResouces returns the life resouces of container.
func (c *Container) LifeResouces(key string) any {
	return c.lifeResouces[key]
}

// LabelHostNamespace returns namespace label
func (c *Container) LabelHostNamespace() string {
	return c.Labels[labelHostNamespace].(string)
}

// getContainers returns the containers by type and level.
func getContainers(typeMask ContainerType, minLevel ContainerQos) (map[string]*Container, error) {
	updatedLock.Lock()
	defer updatedLock.Unlock()

	res := make(map[string]*Container)

	if time.Since(lastUpdatedAt) > updatedStep {
		if err := kubeletSyncContainers(); err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) { // ignore error of no connections
				log.Debugf("failed to sync containers by ECONNREFUSED, err: %v", err)
				return res, nil
			}
			return res, err
		}
		lastUpdatedAt = time.Now()
	}

	log.Debugf("sync latest containers: %+v", containers)
	for _, c := range containers {
		// check Type
		if c.Type&typeMask == 0 {
			continue
		}

		// check Level
		if c.Qos < minLevel {
			continue
		}

		res[c.ID] = c
	}

	return res, nil
}

// GetContainersByType returns the containers by type.
func GetContainersByType(typeMask ContainerType) (map[string]*Container, error) {
	return getContainers(typeMask, ContainerQosLevelMin)
}

// GetNormalContainers returns the normal containers.
func GetNormalContainers() (map[string]*Container, error) {
	return GetContainersByType(ContainerTypeNormal)
}

// GetNormalAndSidecarContainers returns the normal and sidecar containers.
func GetNormalAndSidecarContainers() (map[string]*Container, error) {
	return GetContainersByType(ContainerTypeNormal | ContainerTypeSidecar)
}

// GetAllContainers returns all containers.
func GetAllContainers() (map[string]*Container, error) {
	return getContainers(ContainerTypeAll, ContainerQosLevelMin)
}

// GetContainerByID returns the special container by id.
func GetContainerByID(id string) (*Container, error) {
	all, err := GetAllContainers()
	if err != nil {
		return nil, err
	}

	if c, ok := all[id]; ok {
		return c, nil
	}
	return nil, nil
}

// GetContainerByIPAddress returns the special container by the container ip address.
func GetContainerByIPAddress(ip string) (*Container, error) {
	// only for normal
	all, err := GetNormalContainers()
	if err != nil {
		return nil, err
	}

	for _, c := range all {
		if c.IPAddress == ip {
			return c, nil
		}
	}

	return nil, nil
}

// GetContainerByNetNamespaceInode returns the special container by the net namespace inode.
func GetContainerByNetNamespaceInode(inode uint64) (*Container, error) {
	// only for normal
	all, err := GetNormalContainers()
	if err != nil {
		return nil, err
	}

	for _, c := range all {
		if c.NetNamespaceInode == inode {
			return c, nil
		}
	}

	return nil, nil
}

// GetContainerByCSS returns the special container by the css address.
func GetContainerByCSS(css uint64, subsys string) (*Container, error) {
	all, err := GetAllContainers()
	if err != nil {
		return nil, err
	}

	for _, c := range all {
		if addr, ok := c.CSS[subsys]; ok {
			if addr == css {
				return c, nil
			}
		}
	}

	return nil, nil
}

// BuildCSSToContainerID builds a css-address map from the provided containers.
func BuildCSSToContainerID(containers map[string]*Container, subsys string) map[uint64]string {
	cssToContainerMap := make(map[uint64]string, len(containers))
	for _, container := range containers {
		if addr, ok := container.CSS[subsys]; ok {
			cssToContainerMap[addr] = container.ID
		}
	}
	return cssToContainerMap
}
