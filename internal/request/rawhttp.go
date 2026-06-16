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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"syscall"
)

const (
	cmsgDataMax = 200 * 4
	dataMax     = 3072
)

// RawHTTP is a structure that describes a Info of raw Http
type RawHTTP struct {
	conn   *net.UnixConn
	method string
}

func parsePerfFds(data []byte) ([]int, error) {
	// parse control msgs
	var msgs []syscall.SocketControlMessage
	msgs, err := syscall.ParseSocketControlMessage(data)
	if err != nil {
		return nil, err
	}

	resFds := []int{}
	for i := range msgs {
		var fds []int
		fds, err := syscall.ParseUnixRights(&msgs[i])
		if err != nil {
			return nil, err
		}

		resFds = append(resFds, fds...)
	}

	return resFds, nil
}

func makeRequestRawBuf(method, url, sid string, body io.Reader) (*bytes.Buffer, error) {
	// send request
	sbuf := new(bytes.Buffer)

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-HUATUO-SID", sid)
	req.Header.Set("Content-Type", "application/json")

	if err := req.Write(sbuf); err != nil {
		return nil, err
	}

	return sbuf, err
}

func connectUnix(sock string) (*net.UnixConn, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}

	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("expected *net.UnixConn, got %T", c)
	}

	// auto clean
	runtime.SetFinalizer(uc, (*net.UnixConn).Close)

	return uc, nil
}

// SendRequest Provide a standard interface for sending http to the security agent
func (h *RawHTTP) SendRequest(sock, method, url, sid string, body io.Reader) error {
	conn, err := connectUnix(sock)
	if err != nil {
		return err
	}

	buf, err := makeRequestRawBuf(method, url, sid, body)
	if err != nil {
		return err
	}

	// write raw http data
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return err
	}

	h.conn = conn
	h.method = method

	return nil
}

func setCmsgtoResp(resp *http.Response, fds []int) (*http.Response, error) {
	// Resp.Body we will re assign the value so close it
	resp.Body.Close()

	// Construct body info, we only transfer fds
	body := make(map[string]any)
	body["fds"] = &fds

	newbody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	resp.Body = io.NopCloser(bytes.NewReader(newbody))
	return resp, nil
}

func noExistCmsg(length int) bool {
	// If length is 0, it means that data is only http response data,
	return length == 0
}

func handlerResponse(method string, data []byte) (*http.Response, error) {
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), &http.Request{Method: method})
}

func handlerCmsgAndResponse(method string, requestFD int, cmsgData []byte) (*http.Response, error) {
	// When the count of CPUs exceeds 200, there can receive multiple responses within cmsgs.
	var allFds []int

	// The first response contains cmsg.
	fds, err := parsePerfFds(cmsgData)
	if err != nil {
		return nil, err
	}
	allFds = append(allFds, fds...)

	for {
		// Read the next response which could contain cmsg.
		httpData, cmsgData, err := readCmsgAndResponse(requestFD)
		if err != nil {
			return nil, err
		}

		// Judge cmsgData to determine whether data is only HTTP response data, and exit
		// the loop.
		if noExistCmsg(len(cmsgData)) {
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(httpData)), &http.Request{Method: method})
			if err != nil {
				return nil, err
			}

			return setCmsgtoResp(resp, allFds)
		}

		// Parse cmsg
		fds, err := parsePerfFds(cmsgData)
		if err != nil {
			return nil, err
		}
		allFds = append(allFds, fds...)
	}
}

// readCmsgAndResponse Provide a standard interface for reading cmsg and http response to
// the security agent.
func readCmsgAndResponse(fd int) ([]byte, []byte, error) {
	// The data is divided into two parts:
	// 1. [:cmsgLen] as cmsg
	// 2. [cmsgLen:] as http response data
	data := make([]byte, dataMax)
	cmsgLen := syscall.CmsgSpace(cmsgDataMax)

	cmsgData := data[:cmsgLen]
	httpData := data[cmsgLen:]

	// Read data: There are two cases here
	// 1. If cLen is 0, only HTTP response data is received.
	// 2. If cLen is not 0, it means that cmsg has been received and next recv HTTP response.
	_, cLen, _, _, err := syscall.Recvmsg(fd, httpData, cmsgData, 0)
	if err != nil {
		return nil, nil, err
	}

	return httpData, cmsgData[:cLen], nil
}

// ReadResponse Provide a standard interface for reading http response to the security agent
func (h *RawHTTP) ReadResponse() (*http.Response, error) {
	connFile, err := h.conn.File()
	if err != nil {
		return nil, err
	}

	defer connFile.Close()
	defer h.conn.Close()

	httpData, cmsgData, err := readCmsgAndResponse(int(connFile.Fd()))
	if err != nil {
		return nil, err
	}

	// Judge cLen to determine whether data is only HTTP response data
	if noExistCmsg(len(cmsgData)) {
		return handlerResponse(h.method, httpData)
	}

	// set cmsg to response body
	return handlerCmsgAndResponse(h.method, int(connFile.Fd()), cmsgData)
}

// EncodeBody encode the body to io.Reader for Post body
func EncodeBody(obj any) (io.Reader, error) {
	if obj == nil {
		return nil, nil
	}
	body, err := encodeData(obj)
	if err != nil {
		return nil, err
	}

	return body, nil
}
