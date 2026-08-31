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

package handlers

import (
	"fmt"

	"github.com/google/uuid"

	"huatuo-bamai/internal/timeutil"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	pkgtypes "huatuo-bamai/pkg/types"
)

// DocumentToWatchEvent converts a validated event document into CloudEvents 1.0.
func DocumentToWatchEvent(doc *tracingstore.Document) pkgtypes.WatchEvent {
	observedTimestamp := timeutil.FormatUTC(*doc.ObservedTimestamp)
	return pkgtypes.WatchEvent{
		SpecVersion:     "1.0",
		ID:              uuid.New().String(),
		Source:          fmt.Sprintf("/huatuo/%s/%s", doc.Hostname, doc.TracerName),
		Type:            "tech.huatuo.kernel.event",
		DataContentType: "application/json",
		Time:            observedTimestamp,
		Data: pkgtypes.WatchEventData{
			Hostname:               doc.Hostname,
			Region:                 doc.Region,
			ObservedTimestamp:      observedTimestamp,
			ContainerID:            doc.ContainerID,
			ContainerHostname:      doc.ContainerHostname,
			ContainerHostNamespace: doc.ContainerHostNamespace,
			ContainerType:          doc.ContainerType,
			ContainerQos:           doc.ContainerQoS,
			TracerName:             doc.TracerName,
			TracerID:               doc.TracerID,
			TracerRunType:          doc.TracerRunType,
		},
	}
}
