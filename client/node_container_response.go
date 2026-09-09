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
	"encoding/json"
	"fmt"
	"net/http"

	nodeapi "huatuo-bamai/apis/v1/node"
)

func parseNodeContainerResponse(
	response *http.Response,
	containerID string,
) (*nodeapi.ContainerMetadata, error) {
	defer response.Body.Close()

	limit := int64(maxNodeErrorBodyBytes)
	if response.StatusCode == http.StatusOK {
		limit = maxNodeSuccessBodyBytes
	}
	body, err := readNodeResponseBody(response, limit)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, parseNodeError(response.StatusCode, body)
	}

	var envelope nodeapi.ContainerResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, wrapNodeProtocolError(response.StatusCode, "decode container response", err)
	}
	metadata := envelope.Data
	if metadata.ID != containerID {
		return nil, newNodeProtocolError(
			response.StatusCode,
			fmt.Sprintf("response container ID %q does not match %q", metadata.ID, containerID),
		)
	}
	return &metadata, nil
}
