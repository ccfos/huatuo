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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"huatuo-bamai/internal/timeutil"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	"huatuo-bamai/pkg/types"
)

func TestDocumentToWatchEvent(t *testing.T) {
	document := newTestDocument("cpu", types.TracerRunTypeEvent)
	event := documentToWatchEvent(document)

	if event.SpecVersion != "1.0" || event.Type != "tech.huatuo.kernel.event" ||
		event.DataContentType != "application/json" {
		t.Errorf("documentToWatchEvent() envelope = %+v", event)
	}
	if event.ID == "" {
		t.Error("documentToWatchEvent() ID is empty")
	}
	if !strings.HasPrefix(event.Source, "/huatuo/node-1/cpu") {
		t.Errorf("documentToWatchEvent() source = %q", event.Source)
	}
	if event.Time != timeutil.FormatUTC(*document.ObservedTimestamp) {
		t.Errorf("documentToWatchEvent() time = %q", event.Time)
	}

	want := types.WatchEventData{
		Hostname:          document.Hostname,
		Region:            document.Region,
		ObservedTimestamp: timeutil.FormatUTC(*document.ObservedTimestamp),
		TracerName:        document.TracerName,
		TracerRunType:     document.TracerRunType,
	}
	if diff := cmp.Diff(want, event.Data); diff != "" {
		t.Errorf("documentToWatchEvent() data mismatch (-want +got):\n%s", diff)
	}
	if event.ID == documentToWatchEvent(document).ID {
		t.Error("documentToWatchEvent() reused an event ID")
	}
}

func newTestDocument(tracerName, runType string) *tracingstore.Document {
	observedTimestamp := time.Unix(1_700_000_000, 0).UTC()
	startedTimestamp := observedTimestamp
	return &tracingstore.Document{Document: types.Document{
		Hostname:          "node-1",
		Region:            "cn",
		StartedTimestamp:  &startedTimestamp,
		ObservedTimestamp: &observedTimestamp,
		TracerName:        tracerName,
		TracerRunType:     runType,
	}}
}
