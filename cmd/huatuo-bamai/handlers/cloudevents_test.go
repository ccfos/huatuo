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
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/timeutil"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	pkgtypes "huatuo-bamai/pkg/types"

	"github.com/stretchr/testify/require"
)

func newTestDocument() *tracingstore.Document {
	observedTimestamp := time.Unix(1_700_000_000, 0).UTC()
	return &tracingstore.Document{Document: pkgtypes.Document{
		Hostname:          "node-1",
		Region:            "cn",
		ObservedTimestamp: &observedTimestamp,
		TracerName:        "cpu",
		TracerRunType:     pkgtypes.TracerRunTypeEvent,
	}}
}

func TestDocumentToWatchEvent_CloudEventsFields(t *testing.T) {
	doc := newTestDocument()
	ev := DocumentToWatchEvent(doc)

	require.Equal(t, "1.0", ev.SpecVersion)
	require.Equal(t, "tech.huatuo.kernel.event", ev.Type)
	require.Equal(t, "application/json", ev.DataContentType)
	require.NotEmpty(t, ev.ID)
	require.True(t, strings.HasPrefix(ev.Source, "/huatuo/node-1/cpu"))
	require.Equal(t, timeutil.FormatUTC(*doc.ObservedTimestamp), ev.Time)
}

func TestDocumentToWatchEvent_UniqueIDs(t *testing.T) {
	doc := newTestDocument()
	require.NotEqual(t, DocumentToWatchEvent(doc).ID, DocumentToWatchEvent(doc).ID)
}

func TestDocumentToWatchEvent_Data(t *testing.T) {
	doc := newTestDocument()
	ev := DocumentToWatchEvent(doc)

	want := pkgtypes.WatchEventData{
		Hostname:          doc.Hostname,
		Region:            doc.Region,
		ObservedTimestamp: timeutil.FormatUTC(*doc.ObservedTimestamp),
		TracerName:        doc.TracerName,
		TracerRunType:     doc.TracerRunType,
	}
	require.Equal(t, want, ev.Data)
}
