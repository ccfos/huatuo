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

package storage

import (
	"os"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
)

// standard document.
type document struct {
	Hostname     string    `json:"hostname"`
	Region       string    `json:"region"`
	UploadedTime time.Time `json:"uploaded_time"`
	// equal to `TracerTime`, supported the old version.
	Time string `json:"time"`

	ContainerID            string `json:"container_id,omitempty"`
	ContainerHostname      string `json:"container_hostname,omitempty"`
	ContainerHostNamespace string `json:"container_host_namespace,omitempty"`
	ContainerType          string `json:"container_type,omitempty"`
	ContainerQos           string `json:"container_qos,omitempty"`

	TracerName    string `json:"tracer_name,omitempty"`
	TracerID      string `json:"tracer_id,omitempty"`
	TracerTime    string `json:"tracer_time"`
	TracerRunType string `json:"tracer_type,omitempty"`
	TracerData    any    `json:"tracer_data,omitempty"`
}

type writer interface {
	Write(doc *document) error
}

const (
	docTracerRunAuto = "auto"
	docTracerRunTask = "task"
)

var (
	esExporter        writer = &null{}
	localFileExporter writer = &null{}
	storageInitCtx    InitContext
)

func createBaseDocument(tracerName, containerID string, tracerTime time.Time, tracerData any) *document {
	// TODO: support for !didi.
	doc := &document{
		ContainerID:  containerID,
		UploadedTime: time.Now(),
		TracerName:   tracerName,
		TracerData:   tracerData,
		Region:       storageInitCtx.Region,
		Hostname:     storageInitCtx.Hostname,
	}

	// equal to `TracerTime`, supported the old version.
	doc.Time = tracerTime.Format("2006-01-02 15:04:05.000 -0700")
	doc.TracerTime = doc.Time

	// container information.
	if containerID != "" {
		container, err := pod.ContainerByID(containerID)
		if err != nil {
			log.Infof("get container by %s: %v", containerID, err)
			return nil
		}
		if container == nil {
			log.Infof("the container %s is not found", containerID)
			return nil
		}

		doc.ContainerID = container.ID[:12]
		doc.ContainerHostname = container.Hostname
		doc.ContainerHostNamespace = container.LabelHostNamespace()
		doc.ContainerType = container.Type.String()
		doc.ContainerQos = container.Qos.String()
	}

	return doc
}

type InitContext struct {
	EsAddresses string // Elasticsearch nodes to use.
	EsUsername  string // Username for HTTP Basic Authentication.
	EsPassword  string // Password for HTTP Basic Authentication.
	EsIndex     string

	LocalPath         string
	LocalRotationSize int
	LocalMaxRotation  int
	Region            string
	Hostname          string

	StorageDisabled bool
}

// InitDefaultClients initializes the default clients, that includes local-file, elasticsearch.
func InitDefaultClients(initCtx *InitContext) (err error) {
	// Storage disabled, directly return
	if initCtx.StorageDisabled {
		log.Infof("elasticsearch and local storage diabled, use null device: %+v", initCtx)
		return nil
	}

	// ES client
	if initCtx.EsAddresses == "" || initCtx.EsUsername == "" || initCtx.EsPassword == "" {
		log.Warnf("elasticsearch storage config invalid, use null device: %+v", initCtx)
	} else {
		esclient, err := newESClient(initCtx.EsAddresses, initCtx.EsUsername, initCtx.EsPassword, initCtx.EsIndex)
		if err != nil {
			return err
		}

		esExporter = esclient
	}

	// Local-file client
	if initCtx.LocalPath == "" {
		log.Warnf("localfile storage config invalid, use null device: %+v", initCtx)
	} else {
		localFileClient, err := newLocalFileStorage(initCtx.LocalPath, initCtx.LocalMaxRotation, initCtx.LocalRotationSize)
		if err != nil {
			return err
		}

		localFileExporter = localFileClient
	}

	storageInitCtx = *initCtx
	storageInitCtx.Hostname, _ = os.Hostname()

	log.Info("InitDefaultClients includes engines: elasticsearch, local-file")
	return nil
}

// Save data to the default clients.
func Save(tracerName, containerID string, tracerTime time.Time, tracerData any) {
	document := createBaseDocument(tracerName, containerID, tracerTime, tracerData)
	if document == nil {
		return
	}

	document.TracerRunType = docTracerRunAuto

	// save into es.
	if err := esExporter.Write(document); err != nil {
		log.Infof("failed to save %#v into es: %v", document, err)
	}

	// save into local-file.
	if err := localFileExporter.Write(document); err != nil {
		log.Infof("failed to save %#v into local-file: %v", document, err)
	}
}

type TracerBasicData struct {
	Output string `json:"output"`
}

// SaveTaskOutput saves the tracer output data
func SaveTaskOutput(tracerName, tracerID, containerID string, tracerTime time.Time, tracerData string) {
	document := createBaseDocument(tracerName, containerID, tracerTime, &TracerBasicData{Output: tracerData})
	if document == nil {
		return
	}

	document.TracerRunType = docTracerRunTask
	document.TracerID = tracerID

	// save into es.
	if err := esExporter.Write(document); err != nil {
		log.Infof("failed to save %#v into es: %v", document, err)
	}
}
