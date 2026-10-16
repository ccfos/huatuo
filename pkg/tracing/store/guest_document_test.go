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

package store

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestDecodeDocumentPreservesGuestNumbers(t *testing.T) {
	data := []byte(`{"source_type":"kata_guest","tracer_type":"event","tracer_id":"guest-1","observed_timestamp":"2026-10-16T01:02:03.123456789Z","uploaded_timestamp":"2026-10-16T01:02:04.000000000Z","tracer_data":{"counter":18446744073709551615,"nested":[9007199254740993]}}`)
	doc, err := DecodeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	values := doc.TracerData.(map[string]any)
	if values["counter"] != json.Number("18446744073709551615") || values["nested"].([]any)[0] != json.Number("9007199254740993") {
		t.Errorf("JSON numbers rounded: %+v", values)
	}
	mapped, err := (mapper{}).Decode(driver.Record{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := (mapper{}).Encode(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("18446744073709551615")) || mapped.SourceType != types.DocumentSourceKataGuest {
		t.Errorf("round trip lost fields: %s", encoded)
	}
	fields, err := (mapper{}).Fields(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if fields[types.DocumentFieldSourceType] != types.DocumentSourceKataGuest {
		t.Errorf("missing origin index field: %v", fields)
	}
	store, err := NewFromConfig(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(t.Context())
	events, cancel := store.Subscribe()
	defer cancel()
	oldUpload := doc.UploadedTimestamp.Time
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if doc.UploadedTimestamp.Equal(oldUpload) || doc.ObservedTimestamp.FormatUTC() != "2026-10-16T01:02:03.123456789Z" || doc.KernelObservedTimestamp != nil || doc.StartedTimestamp != nil {
		t.Errorf("clock ownership changed: %+v", doc.Document)
	}
	select {
	case got := <-events:
		if got != doc {
			t.Error("prebuilt document did not reach subscribers")
		}
	case <-time.After(time.Second):
		t.Error("prebuilt event was not published")
	}
}

func TestDecodeDocumentRejectsTrailingAndNullJSON(t *testing.T) {
	for _, data := range []string{"null", "{} {}", "{} null", "{} garbage", "[]", ""} {
		if _, err := DecodeDocument([]byte(data)); err == nil {
			t.Errorf("DecodeDocument(%q) accepted invalid document", data)
		}
	}
	if _, err := DecodeDocument([]byte("{} \n\t")); err != nil {
		t.Errorf("valid trailing whitespace rejected: %v", err)
	}
}
