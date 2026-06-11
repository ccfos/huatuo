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

package profiler

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"
)

const profilingMetadataCollection = "profiling_metadata"

const profilingMetadataTracerRunManual = "manual"

type ProfilingDocumentMapper struct{}

var profilingMetadataContainerLookupFunc = fetchProfilingMetadataContainer

func CreateProfilingDocument(metadata map[string]string, containerID, serverAddress string) *tracing.Document {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "huatuo"
	}

	uploadedTime := time.Now()
	tracerTime := time.Now()
	formattedTime := tracerTime.Format("2006-01-02 15:04:05.000 -0700")
	tracerID := metadata["tracer_id"]

	document := &tracing.Document{
		Hostname:     hostname,
		UploadedTime: uploadedTime,
		Time:         formattedTime,
		TracerTime:   formattedTime,
		TracerID:     tracerID,
	}

	if tracerID == "" {
		document.TracerRunType = profilingMetadataTracerRunManual
	} else {
		document.Region = metadata["region"]
		document.TracerRunType = metadata["tracer_type"]
		document.TracerName = metadata["tracer_name"]
	}

	if containerID != "" {
		container, err := profilingMetadataContainerLookupFunc(serverAddress, containerID)
		if err != nil {
			log.Infof("get container by %s: %v", containerID, err)
			return nil
		}
		if container == nil {
			log.Infof("the container %s is not found", containerID)
			return nil
		}

		document.ContainerID = shortenProfilingMetadataContainerID(container.ID)
		document.ContainerHostname = container.Hostname
		document.ContainerHostNamespace = container.HostNamespace
		document.ContainerType = container.Type
		document.ContainerQoS = container.QOS
	}

	if raw := metadata["mock_container"]; raw != "" {
		applyProfilingMetadataMockContainer(document, raw)
	}

	return document
}

func (ProfilingDocumentMapper) Collection() string {
	return profilingMetadataCollection
}

func (ProfilingDocumentMapper) ID(document *tracing.Document) string {
	return document.TracerID
}

func (ProfilingDocumentMapper) Encode(document *tracing.Document) ([]byte, error) {
	return json.Marshal(document)
}

func (ProfilingDocumentMapper) Decode(data []byte) (*tracing.Document, error) {
	var document tracing.Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	return &document, nil
}

func (ProfilingDocumentMapper) Fields(document *tracing.Document) (map[string]any, error) {
	return map[string]any{
		"record_id":                document.TracerID,
		"hostname":                 document.Hostname,
		"region":                   document.Region,
		"uploaded_time":            document.UploadedTime,
		"time":                     profilingMetadataTimeValue(document.Time, document.UploadedTime),
		"container_id":             document.ContainerID,
		"container_hostname":       document.ContainerHostname,
		"container_host_namespace": document.ContainerHostNamespace,
		"container_type":           document.ContainerType,
		"container_qos":            document.ContainerQoS,
		"tracer_name":              document.TracerName,
		"tracer_id":                document.TracerID,
		"tracer_time":              profilingMetadataTimeValue(document.TracerTime, document.UploadedTime),
		"tracer_type":              document.TracerRunType,
		"profile_type":             extractProfilingMetadataProfileType(document.TracerData),
	}, nil
}

func (ProfilingDocumentMapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: "record_id"},
		{Field: "hostname"},
		{Field: "region"},
		{Field: "uploaded_time"},
		{Field: "time"},
		{Field: "container_id"},
		{Field: "container_hostname"},
		{Field: "container_host_namespace"},
		{Field: "container_type"},
		{Field: "container_qos"},
		{Field: "tracer_name"},
		{Field: "tracer_id"},
		{Field: "tracer_time"},
		{Field: "tracer_type"},
		{Field: "profile_type"},
	}
}

func profilingMetadataTimeValue(raw string, fallback time.Time) time.Time {
	if raw == "" {
		return fallback.UTC()
	}

	parsed, err := time.Parse("2006-01-02 15:04:05.000 -0700", raw)
	if err == nil {
		return parsed.UTC()
	}

	return fallback.UTC()
}

func extractProfilingMetadataProfileType(tracerData any) string {
	if tracerData == nil {
		return ""
	}

	raw, err := json.Marshal(tracerData)
	if err != nil {
		return ""
	}

	var payload struct {
		FlameData struct {
			ProfileType string `json:"profile_type,omitempty"`
		} `json:"flamedata,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}

	return payload.FlameData.ProfileType
}

type profilingMetadataMockContainer struct {
	ContainerID            string `json:"container_id"`
	ContainerHostname      string `json:"container_hostname"`
	ContainerHostNamespace string `json:"container_host_namespace"`
	ContainerType          string `json:"container_type"`
	ContainerQOS           string `json:"container_qos"`
	Region                 string `json:"region"`
}

type profilingMetadataContainer struct {
	ID            string
	Hostname      string
	HostNamespace string
	Type          string
	QOS           string
}

func fetchProfilingMetadataContainer(serverAddress, containerID string) (*profilingMetadataContainer, error) {
	if serverAddress == "" || containerID == "" {
		return nil, fmt.Errorf("container lookup requires server address and container id")
	}

	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/containers/json?container_id=%s", serverAddress, containerID), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get container failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, fmt.Errorf("get container failed, status code: %d, read body: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("get container failed, status code: %d, body: %s", response.StatusCode, string(body))
	}

	var payload struct {
		Data []struct {
			ID       string         `json:"id"`
			Hostname string         `json:"hostname"`
			Type     any            `json:"type"`
			QOS      any            `json:"qos"`
			Labels   map[string]any `json:"labels"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode container response failed: %w", err)
	}

	if len(payload.Data) == 0 {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	container := payload.Data[0]
	return &profilingMetadataContainer{
		ID:            container.ID,
		Hostname:      container.Hostname,
		HostNamespace: stringFromMetadataValue(container.Labels["HostNamespace"]),
		Type:          stringFromMetadataValue(container.Type),
		QOS:           stringFromMetadataValue(container.QOS),
	}, nil
}

func shortenProfilingMetadataContainerID(containerID string) string {
	if len(containerID) <= 12 {
		return containerID
	}
	return containerID[:12]
}

func stringFromMetadataValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func applyProfilingMetadataMockContainer(document *tracing.Document, raw string) {
	if raw == "random" {
		raw = randomProfilingMetadataMockContainerJSON()
	}

	var mock profilingMetadataMockContainer
	if err := json.Unmarshal([]byte(raw), &mock); err != nil {
		log.Infof("invalid mock-container metadata: %v", err)
		return
	}

	if mock.ContainerID != "" {
		document.ContainerID = mock.ContainerID
	}
	if mock.ContainerHostname != "" {
		document.ContainerHostname = mock.ContainerHostname
	}
	if mock.ContainerHostNamespace != "" {
		document.ContainerHostNamespace = mock.ContainerHostNamespace
	}
	if mock.ContainerType != "" {
		document.ContainerType = mock.ContainerType
	}
	if mock.ContainerQOS != "" {
		document.ContainerQoS = mock.ContainerQOS
	}
	if mock.Region != "" {
		document.Region = mock.Region
	}
}

func randomProfilingMetadataMockContainerJSON() string {
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	mock := profilingMetadataMockContainer{
		ContainerID:            randomProfilingMetadataHex(randomSource, 12),
		ContainerHostname:      fmt.Sprintf("mock-container-%s", randomProfilingMetadataHex(randomSource, 6)),
		ContainerHostNamespace: fmt.Sprintf("mock-ns-%s", randomProfilingMetadataHex(randomSource, 6)),
		ContainerType:          "Normal",
		ContainerQOS:           "102",
		Region:                 fmt.Sprintf("mock-%s", randomProfilingMetadataHex(randomSource, 4)),
	}

	data, err := json.Marshal(&mock)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func randomProfilingMetadataHex(randomSource *rand.Rand, length int) string {
	const hex = "0123456789abcdef"

	buffer := make([]byte, length)
	for index := range buffer {
		buffer[index] = hex[randomSource.Intn(len(hex))]
	}

	return string(buffer)
}
