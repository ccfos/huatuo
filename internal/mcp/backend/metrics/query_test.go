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

package metrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestBoundResponsePreservesWarningsAndReportsTruncation(t *testing.T) {
	data := []byte(`{"status":"success","warnings":["backend partial"],"data":{"resultType":"matrix","result":[{"metric":{"id":"1"}},{"metric":{"id":"2"}}]}}`)
	bounded, err := BoundResponse(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Truncated bool     `json:"mcp_truncated"`
		Warnings  []string `json:"warnings"`
		Data      struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bounded.Response, &response); err != nil {
		t.Fatal(err)
	}
	if !bounded.Truncated || bounded.Count != 1 || !response.Truncated || len(response.Data.Result) != 1 || len(response.Warnings) != 2 || response.Warnings[0] != "backend partial" || !reflect.DeepEqual(bounded.Warnings, response.Warnings) {
		t.Fatalf("truncated result=%+v body=%s", bounded, bounded.Response)
	}
	complete, err := BoundResponse(data, 2)
	if err != nil || complete.Truncated || complete.Count != 2 || !bytes.Equal(data, complete.Response) || !reflect.DeepEqual(complete.Warnings, []string{"backend partial"}) {
		t.Fatalf("complete result=%+v, %v", complete, err)
	}
}

func TestBoundResponseExposesWarningsWithoutTruncation(t *testing.T) {
	for _, result := range []string{
		`{"resultType":"matrix","result":[]}`,
		`{"resultType":"scalar","result":[1700000000,"1"]}`,
	} {
		data := []byte(`{"status":"success","warnings":["backend partial"],"data":` + result + `}`)
		bounded, err := BoundResponse(data, 10)
		if err != nil || bounded.Truncated || !bytes.Equal(bounded.Response, data) || !reflect.DeepEqual(bounded.Warnings, []string{"backend partial"}) {
			t.Fatalf("warning result=%+v, %v", bounded, err)
		}
	}
	if _, err := BoundResponse([]byte(`{"status":"success","warnings":{},"data":{"resultType":"matrix","result":[]}}`), 10); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("invalid warnings accepted: %v", err)
	}
}

func TestBoundResponsePreservesScalarAndRejectsMalformedData(t *testing.T) {
	for _, test := range []struct {
		data  string
		count int
	}{
		{`{"resultType":"scalar","result":[1700000000,"1"]}`, 1},
		{`{"resultType":"scalar","result":[1700000000,"NaN"]}`, 1},
		{`{"resultType":"scalar","result":[1700000000,"+Inf"]}`, 1},
		{`{"resultType":"string","result":[1700000000,"text"]}`, 1},
		{`{"resultType":"string","result":[1700000000,""]}`, 1},
		{`{"resultType":"matrix","result":[]}`, 0},
		{`{"resultType":"vector","result":[]}`, 0},
	} {
		data := []byte(`{"status":"success","data":` + test.data + `}`)
		result, err := BoundResponse(data, 1)
		if err != nil || result.Count != test.count || result.Truncated || !bytes.Equal(data, result.Response) {
			t.Fatalf("valid response %s: result=%+v, %v", data, result, err)
		}
	}
	for _, data := range []string{
		`{`,
		`{"status":"success"}`,
		`{"status":"success","data":null}`,
		`{"status":"success","data":{}}`,
		`{"status":"success","data":{"resultType":"matrix"}}`,
		`{"status":"success","data":{"resultType":"matrix","result":null}}`,
		`{"status":"success","data":{"resultType":"vector","result":null}}`,
		`{"status":"success","data":{"resultType":"scalar","result":null}}`,
		`{"status":"success","data":{"resultType":"string","result":null}}`,
		`{"status":"success","data":{"result":[]}}`,
		`{"status":"success","data":{"resultType":null,"result":[]}}`,
		`{"status":"success","data":{"resultType":"unknown","result":[]}}`,
		`{"status":"success","data":{"resultType":"matrix","result":{}}}`,
		`{"status":"success","data":{"resultType":"vector","result":{}}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[]}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[null,"1"]}}`,
		`{"status":"success","data":{"resultType":"scalar","result":["1700000000","1"]}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[1700000000,1]}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[1700000000,"text"]}}`,
		`{"status":"success","data":{"resultType":"string","result":[1700000000,null]}}`,
		`{"status":"success","data":{"resultType":"string","result":[1700000000,1]}}`,
		`{"status":"success","data":{"resultType":"string","result":[1700000000,"text","extra"]}}`,
	} {
		if result, err := BoundResponse([]byte(data), 1); !errors.Is(err, ErrDecodeFailed) || len(result.Response) != 0 {
			t.Errorf("malformed response accepted: %s, result=%+v, %v", data, result, err)
		}
	}
}

func TestStatisticsResponseRequiresInstantSamplesAndRejectsOverflow(t *testing.T) {
	for _, data := range []string{
		`{"status":"success","data":{"resultType":"vector","result":[]}}`,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"host":"node-a"},"value":[1,"NaN"]}]}}`,
		`{"status":"success","warnings":["partial"],"data":{"resultType":"scalar","result":[1,"2"]}}`,
	} {
		result, err := StatisticsResponse([]byte(data), 1)
		if err != nil || result.Truncated || !bytes.Equal(result.Response, []byte(data)) {
			t.Errorf("valid statistics modified or rejected: %+v %v", result, err)
		}
	}
	for _, data := range []string{
		`{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		`{"status":"success","data":{"resultType":"vector","result":null}}`,
		`{"status":"success","data":{"resultType":"vector"}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[null,"2"]}}`,
		`{"status":"success","data":{"resultType":"scalar","result":[1,"bad-value"]}}`,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,2]}]}}`,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[]}]}}`,
	} {
		if _, err := StatisticsResponse([]byte(data), 1); !errors.Is(err, ErrDecodeFailed) {
			t.Errorf("invalid statistics accepted: %s, %v", data, err)
		}
	}
	data := []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`)
	if result, err := StatisticsResponse(data, 1); !errors.Is(err, ErrInvalidRequest) || len(result.Response) != 0 {
		t.Fatalf("overflow returned truncated statistics: %+v %v", result, err)
	}
}
