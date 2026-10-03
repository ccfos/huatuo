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

package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
)

func TestRawProfileResponseTimestampFormat(t *testing.T) {
	instant := time.Date(2026, 9, 17, 8, 0, 0, 123000000, time.FixedZone("local", 8*60*60))
	profile := RawProfile{Hostname: "host", UploadedTimestamp: timeutil.Timestamp{Time: instant}, StartedTimestamp: timeutil.Timestamp{Time: instant}, ProfileType: "cpu", Profile: map[string]string{"kept": "payload"}}
	response := GetRawProfiles200JSONResponse{Data: RawProfilePage{Items: []RawProfile{profile}}}
	recorder := httptest.NewRecorder()
	if err := response.VisitGetRawProfilesResponse(recorder); err != nil {
		t.Fatal(err)
	}
	encoded := recorder.Body.Bytes()
	text := string(encoded)
	if strings.Count(text, "2026-09-17T00:00:00.123000000Z") != 2 || !strings.Contains(text, `"kept":"payload"`) {
		t.Fatalf("invalid response: %s", text)
	}
	var decoded RawProfilePageResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Data.Items[0].StartedTimestamp.Equal(instant) {
		t.Fatal("timestamp changed")
	}
}
