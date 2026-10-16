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
	"encoding/json"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestGuestProfilingDocumentRoundTrip(t *testing.T) {
	document := validDocument()
	document.SourceType = types.DocumentSourceKataGuest
	document.ProfileData.Profile.TimeNanos = 9007199254740993
	document.ProfileData.Profile.Sample = []*profilev1.Sample{{Value: []int64{9007199254740993}}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProfileData.Profile.TimeNanos != 9007199254740993 || decoded.ProfileData.Profile.Sample[0].Value[0] != 9007199254740993 {
		t.Errorf("profile integers rounded: %+v", decoded.ProfileData.Profile)
	}
	store, backend := newTestStore(t)
	started := *decoded.StartedTimestamp
	if err := store.SaveSync(t.Context(), decoded); err != nil {
		t.Fatal(err)
	}
	if backend.syncSaves != 1 || decoded.UploadedTimestamp.IsZero() || !decoded.StartedTimestamp.Equal(started.Time) || decoded.ObservedTimestamp != nil {
		t.Errorf("profiling save lost ownership: %+v", decoded.Document)
	}
	fields, err := (mapper{}).Fields(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if fields[types.DocumentFieldSourceType] != types.DocumentSourceKataGuest {
		t.Error("profile origin was not indexed")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (mapper{}).Decode(driver.Record{Data: encoded}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilingDecodeRejectsTrailingAndNullJSON(t *testing.T) {
	for _, data := range []string{"null", "{} {}", "{} null", "{} garbage", "[]", ""} {
		if _, err := DecodeDocument([]byte(data)); err == nil {
			t.Errorf("DecodeDocument(%q) accepted invalid document", data)
		}
	}
}
