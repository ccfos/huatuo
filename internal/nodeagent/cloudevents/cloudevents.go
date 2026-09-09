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

package cloudevents

import (
	"fmt"

	"github.com/google/uuid"

	"huatuo-bamai/internal/timeutil"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	"huatuo-bamai/pkg/types"
)

func documentToWatchEvent(document *tracingstore.Document) types.WatchEvent {
	observedTimestamp := timeutil.FormatUTC(*document.ObservedTimestamp)
	return types.WatchEvent{
		SpecVersion:     "1.0",
		ID:              uuid.New().String(),
		Source:          fmt.Sprintf("/huatuo/%s/%s", document.Hostname, document.TracerName),
		Type:            "tech.huatuo.kernel.event",
		DataContentType: "application/json",
		Time:            observedTimestamp,
		Data: types.WatchEventData{
			Hostname:               document.Hostname,
			Region:                 document.Region,
			ObservedTimestamp:      observedTimestamp,
			ContainerID:            document.ContainerID,
			ContainerHostname:      document.ContainerHostname,
			ContainerHostNamespace: document.ContainerHostNamespace,
			ContainerType:          document.ContainerType,
			ContainerQos:           document.ContainerQoS,
			TracerName:             document.TracerName,
			TracerID:               document.TracerID,
			TracerRunType:          document.TracerRunType,
		},
	}
}
