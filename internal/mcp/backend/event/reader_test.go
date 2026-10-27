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

package event

import "testing"

func TestAggregationFieldWhitelistIsOwnedAndShared(t *testing.T) {
	fields := AggregationFields()
	if len(fields) != 4 {
		t.Fatalf("aggregation fields = %v", fields)
	}
	for _, field := range fields {
		if !ValidAggregationField(field) {
			t.Errorf("schema field rejected by validation: %s", field)
		}
	}
	original := fields[0]
	fields[0] = "tracer_data.secret"
	if !ValidAggregationField(original) || ValidAggregationField(fields[0]) || AggregationFields()[0] != original {
		t.Fatal("schema consumer mutated the validation whitelist")
	}
}

func TestRoutedDocumentDoesNotMutateNativeSource(t *testing.T) {
	document := &Document{RecordID: "event-7", Source: &SourceRef{Backend: "external", Region: "stale", Locator: map[string]string{"stream": "cpu"}}}
	document.Region = "stale"
	routed := RoutedDocument(document, "external", "cn")
	routed.Source.Locator["stream"] = "changed"
	if routed.Region != "cn" || routed.Source.Region != "cn" || document.Region != "stale" || document.Source.Region != "stale" || document.Source.Locator["stream"] != "cpu" {
		t.Fatalf("routing mutated reader data: %+v, %+v", document, routed)
	}
}
