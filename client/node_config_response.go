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

package client

import (
	"fmt"
	"net/http"
)

func parseNodeConfigResponse(response *http.Response) error {
	defer response.Body.Close()

	body, err := readNodeResponseBody(response, maxNodeErrorBodyBytes)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusNoContent {
		if len(body) != 0 {
			return newNodeProtocolError(response.StatusCode, "HTTP 204 response contains a body")
		}
		return nil
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return newNodeProtocolError(
			response.StatusCode,
			fmt.Sprintf("unexpected HTTP %d success response", response.StatusCode),
		)
	}
	return parseNodeError(response.StatusCode, body)
}
