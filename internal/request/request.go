// Copyright 2025 The HuaTuo Authors
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

package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
)

const (
	defaultReqTimeout = 10 * time.Second
	scheme            = "http"
	respBodyMax       = 1 * 1024 * 1024 // 1 MiB
)

// ServerResponse is a wrapper for http API responses
type ServerResponse struct {
	Body       io.ReadCloser
	Header     http.Header
	StatusCode int
	ReqURL     *url.URL
}

// ErrorResponse Represents an error.
type ErrorResponse struct {
	// The error message.
	Message string `json:"message"`
}

// Close to close response.Body, it will be automatically released even if it is not actively called
func (sresp *ServerResponse) Close() {
	if sresp.Body == nil {
		return
	}

	sresp.Body.Close()
	sresp.Body = nil
}

func buildRequest(host, method, path string, body io.Reader, headers http.Header) (*http.Request, error) {
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		return nil, err
	}

	if headers != nil {
		req.Header = headers.Clone()
	}

	// add authorization to header
	req.URL.Host = host
	req.URL.Scheme = scheme

	return req, nil
}

func doRequest(req *http.Request) (*ServerResponse, error) {
	serverResp := &ServerResponse{StatusCode: -1, ReqURL: req.URL}
	defer runtime.SetFinalizer(serverResp, (*ServerResponse).Close)

	client := &http.Client{
		Timeout: defaultReqTimeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		return serverResp, err
	}

	serverResp.StatusCode = resp.StatusCode
	serverResp.Body = resp.Body
	serverResp.Header = resp.Header

	return serverResp, nil
}

func sendRequest(host, method, path string, query url.Values, body io.Reader, headers http.Header) (*ServerResponse, error) {
	p := (&url.URL{Path: path, RawQuery: query.Encode()}).String()
	req, err := buildRequest(host, method, p, body, headers)
	if err != nil {
		return nil, err
	}

	resp, err := doRequest(req)
	if err != nil {
		return resp, err
	}

	if err = checkResponseErr(resp); err != nil {
		return resp, err
	}

	return resp, nil
}

func encodeData(data any) (*bytes.Buffer, error) {
	params := bytes.NewBuffer(nil)
	if data != nil {
		if err := json.NewEncoder(params).Encode(data); err != nil {
			return nil, err
		}
	}

	return params, nil
}

func encodeBody(obj any, headers http.Header) (io.Reader, http.Header, error) {
	if obj == nil {
		return nil, headers, nil
	}
	body, err := encodeData(obj)
	if err != nil {
		return nil, headers, err
	}

	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")

	return body, headers, nil
}

func checkResponseErr(serverResp *ServerResponse) error {
	if serverResp.StatusCode >= http.StatusOK && serverResp.StatusCode < http.StatusBadRequest {
		return nil
	}

	var body []byte
	var err error
	if serverResp.Body != nil {
		bodyR := &io.LimitedReader{
			R: serverResp.Body,
			N: int64(respBodyMax),
		}
		body, err = io.ReadAll(bodyR)
		if err != nil {
			return err
		}
		if bodyR.N == 0 {
			return fmt.Errorf(`request returned %s with a message (> %d bytes) for API route and version %s,
		 check if the server supports the requested API version`,
				http.StatusText(serverResp.StatusCode), respBodyMax, serverResp.ReqURL)
		}
	}
	if len(body) == 0 {
		return fmt.Errorf(`request returned %s for API route and version %s,
		 check if the server supports the requested API version`,
			http.StatusText(serverResp.StatusCode), serverResp.ReqURL)
	}

	var contentType string
	if serverResp.Header != nil {
		contentType = serverResp.Header.Get("Content-Type")
	}

	var errorMessage string
	var errResponse ErrorResponse
	if !strings.Contains(contentType, "application/json") {
		errorMessage = strings.TrimSpace(string(body))
		return errors.Wrap(errors.New(errorMessage), "Error response from HuaTuo")
	}

	if err := json.Unmarshal(body, &errResponse); err != nil {
		errorMessage = strings.TrimSpace("unmarshal errResponse to JSON failed, output body: " + string(body))
	} else {
		errorMessage = strings.TrimSpace(errResponse.Message)
	}

	return errors.Wrap(errors.New(errorMessage), "Error response from HuaTuo")
}

// HTTPGet Get sends an http request to the huatuo API using the method GET
func HTTPGet(host, path string, query url.Values, headers http.Header) (*ServerResponse, error) {
	return sendRequest(host, http.MethodGet, path, query, nil, headers)
}

// HTTPPut Put sends an http request to the huatuo API using the method PUT
func HTTPPut(host, path string, query url.Values, obj any, headers http.Header) (*ServerResponse, error) {
	body, headers, err := encodeBody(obj, headers)
	if err != nil {
		return nil, err
	}

	return sendRequest(host, http.MethodPut, path, query, body, headers)
}

// HTTPPost Post sends an http request to the huatuo API using the method POST
func HTTPPost(host, path string, query url.Values, obj any, headers http.Header) (*ServerResponse, error) {
	body, headers, err := encodeBody(obj, headers)
	if err != nil {
		return nil, err
	}

	return sendRequest(host, http.MethodPost, path, query, body, headers)
}

// HTTPDelete Delete sends an http request to the huatuo API using the method DELETE
func HTTPDelete(host, path string, query url.Values, headers http.Header) (*ServerResponse, error) {
	return sendRequest(host, http.MethodDelete, path, query, nil, headers)
}

// HTTPErrorMesg deal with http error
func HTTPErrorMesg(body io.ReadCloser) string {
	resp := ErrorResponse{}
	text, _ := io.ReadAll(body)
	err := json.Unmarshal(text, &resp)
	if err != nil {
		fmt.Println("error message json.Unmarshal err: ", err)
	}
	return resp.Message
}