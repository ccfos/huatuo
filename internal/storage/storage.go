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
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage/elasticsearch"
	"huatuo-bamai/internal/storage/localfile"
	"huatuo-bamai/internal/storage/null"
	"huatuo-bamai/internal/storage/types"
)

//go:generate mockery --name=Writer --dir=. --filename=mock_writer_test.go --inpackage --case=underscore
type Writer interface {
	Write(doc *types.Document) error
}

const (
	docTracerRunAuto   = "auto"
	docTracerRunTask   = "task"
	docTracerRunManual = "manual"
)

var (
	esExporter         Writer = &null.StorageClient{}
	localFileExporter  Writer = &null.StorageClient{}
	storageInitCtx     InitContext
	profilerExporterMu sync.RWMutex
)

var containerLookupFunc = pod.ContainerByID

func createBaseDocument(tracerName, containerID string, tracerTime time.Time, tracerData any) *types.Document {
	// TODO: support for !didi.
	doc := &types.Document{
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
		container, err := containerLookupFunc(containerID)
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

func CreateProfilerDocument(pctxMetaData map[string]string, pctxContainerID, pctxServerAddr string) *types.Document {
	// TODO: support for !didi.
	var doc *types.Document
	hostname, _ := os.Hostname()
	uploadedTime := time.Now()
	tracerTime := time.Now()
	// equal to `TracerTime`, supported the old version.
	time := tracerTime.Format("2006-01-02 15:04:05.000 -0700")

	tracerId := pctxMetaData["tracer_id"]
	if tracerId == "" {
		doc = &types.Document{
			Hostname:      hostname,
			UploadedTime:  uploadedTime,
			TracerRunType: docTracerRunManual,
			TracerID:      tracerId,
			TracerTime:    time,
			Time:          time,
		}
	} else {
		doc = &types.Document{
			Hostname:      hostname,
			UploadedTime:  uploadedTime,
			Region:        pctxMetaData["region"],
			TracerRunType: pctxMetaData["tracer_type"],
			TracerName:    pctxMetaData["tracer_name"],
			TracerID:      tracerId,
		}
	}

	containerID := pctxContainerID
	if containerID != "" {
		container, err := container.GetContainerByID(pctxServerAddr, containerID)
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
		esclient, err := elasticsearch.NewStorageClient(initCtx.EsAddresses, initCtx.EsUsername, initCtx.EsPassword, initCtx.EsIndex)
		if err != nil {
			return err
		}

		esExporter = esclient
	}

	// Local-file client
	if initCtx.LocalPath == "" {
		log.Warnf("localfile storage config invalid, use null device: %+v", initCtx)
	} else {
		localFileClient, err := localfile.NewStorageClient(initCtx.LocalPath, initCtx.LocalMaxRotation, initCtx.LocalRotationSize)
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

// InitProfilerClients initializes the profiler client of profiling tools
func InitProfilerClients(esAddress, esUsername, esPassword, esIndex string) (w Writer, err error) {
	profilerExporterMu.Lock()
	defer profilerExporterMu.Unlock()

	// ES client
	var profilerEsExportor Writer
	if esAddress == "" || esUsername == "" || esPassword == "" {
		return nil, fmt.Errorf("elasticsearch storage config invalid, please input correct config")
	} else {
		profilerEsExportor, err = elasticsearch.NewStorageClient(esAddress, esUsername, esPassword, esIndex)
		if err != nil {
			return nil, err
		}
	}

	log.Info("InitProfilerClients includes engines: elasticsearch")
	return profilerEsExportor, nil
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

// SaveTaskJSONOutput saves the tracer output data
func SaveTaskJSONOutput(tracerName, tracerID, containerID string, tracerTime time.Time, tracerData string) {
	// unmarshal tracerData
	var tracerDataMap map[string]any
	if err := json.Unmarshal([]byte(tracerData), &tracerDataMap); err != nil {
		log.Infof("failed to unmarshal tracerData: %v", err)
		return
	}

	document := createBaseDocument(tracerName, containerID, tracerTime, tracerDataMap)
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
