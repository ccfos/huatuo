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

package elasticsearch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func decodeTermsSearchResponse(body io.Reader) (*essearch.Response, error) {
	// The SDK's root decoder creates its own decoder, rounding numeric keys
	// in fallback aggregations even when the caller enables UseNumber.
	type responseWithoutDecoder essearch.Response
	var payload responseWithoutDecoder
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	response := (*essearch.Response)(&payload)
	if _, ok := response.Aggregations["terms"]; !ok {
		for name, aggregation := range response.Aggregations {
			if kind, key, typed := strings.Cut(name, "#"); typed && key == "terms" {
				decoded, err := decodeKnownTerms(kind, aggregation)
				if err != nil {
					return nil, err
				}
				response.Aggregations["terms"] = decoded
				break
			}
		}
	}
	return response, nil
}

func decodeKnownTerms(kind string, aggregation types.Aggregate) (types.Aggregate, error) {
	var decoded types.Aggregate
	switch kind {
	case "sterms":
		decoded = &types.StringTermsAggregate{}
	case "lterms":
		if err := quoteLongTermKeys(aggregation); err != nil {
			return nil, err
		}
		decoded = &types.LongTermsAggregate{}
	case "dterms":
		decoded = &types.DoubleTermsAggregate{}
	case "umterms":
		decoded = &types.UnmappedTermsAggregate{}
	default:
		if fields, ok := aggregation.(map[string]any); ok {
			if _, hasBuckets := fields["buckets"]; !hasBuckets {
				return nil, fmt.Errorf("unexpected terms aggregation type %q", kind)
			}
		}
		return aggregation, nil
	}
	// Keep SDK validation/formatting after normalizing long keys. Generic
	// fallback aggregations retain json.Number for unsigned integer keys.
	data, err := json.Marshal(aggregation)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func quoteLongTermKeys(aggregation types.Aggregate) error {
	fields, ok := aggregation.(map[string]any)
	if !ok {
		return nil
	}
	buckets, _ := fields["buckets"].([]any)
	for _, value := range buckets {
		bucket, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if number, ok := bucket["key"].(json.Number); ok {
			if _, err := number.Int64(); err != nil {
				return fmt.Errorf("invalid signed integer term key %q: %w", number, err)
			}
			// The SDK parses numeric long keys through float64, but its string
			// path uses ParseInt and retains the complete signed integer.
			bucket["key"] = number.String()
		}
	}
	return nil
}

func termString(value any) (string, error) {
	number, ok := value.(json.Number)
	if !ok || !strings.ContainsAny(number.String(), ".eE") {
		return driver.StringValue(value), nil
	}
	// Retain the existing formatting of floating-point terms without routing
	// integer identifiers through float64.
	floating, err := number.Float64()
	if err != nil {
		return "", err
	}
	return driver.StringValue(floating), nil
}
