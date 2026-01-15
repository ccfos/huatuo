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

package filerotate

import (
	"io"

	"gopkg.in/natefinch/lumberjack.v2"
)

type fileRotator struct {
	// Filename is the file to write logs to.  Backup log files will be retained
	// in the same directory.  It uses <processname>-lumberjack.log in
	// os.TempDir() if empty.
	// Backups use the log file name given to Logger, in the form
	// `name-timestamp.ext` where name is the filename without the extension,
	// timestamp is the time at which the log was rotated formatted with the
	// time.Time format of `2006-01-02T15-04-05.000` and the extension is the
	// original extension.  For example, if your Logger.Filename is
	// `/var/log/foo/server.log`, a backup created at 6:30pm on Nov 11 2016 would
	// use the filename `/var/log/foo/server-2016-11-04T18-30-00.000.log`
	//
	// Filename string `json:"filename" yaml:"filename"`

	// MaxSize is the maximum size in megabytes of the log file before it gets
	// rotated. It defaults to 100 megabytes.
	// MaxSize int `json:"maxsize" yaml:"maxsize"`

	// MaxAge is the maximum number of days to retain old log files based on the
	// timestamp encoded in their filename.  Note that a day is defined as 24
	// hours and may not exactly correspond to calendar days due to daylight
	// savings, leap seconds, etc. The default is not to remove old log files
	// based on age.
	// MaxAge int `json:"maxage" yaml:"maxage"`

	// MaxBackups is the maximum number of old log files to retain.  The default
	// is to retain all old log files (though MaxAge may still cause them to get
	// deleted.)
	// MaxBackups int `json:"maxbackups" yaml:"maxbackups"`

	// LocalTime determines if the time used for formatting the timestamps in
	// backup files is the computer's local time.  The default is to use UTC
	// time.
	// LocalTime bool `json:"localtime" yaml:"localtime"`

	// Compress determines if the rotated log files should be compressed
	// using gzip. The default is not to perform compression.
	// Compress bool `json:"compress" yaml:"compress"`
	logger *lumberjack.Logger
}

// NewFileRotator create a rotatable logger
func NewFileRotator(path string, maxRotation, rotationSize int) io.WriteCloser {
	return &fileRotator{
		&lumberjack.Logger{
			Filename:   path,
			MaxSize:    rotationSize,
			MaxBackups: maxRotation,
			LocalTime:  true,
			Compress:   false,
		},
	}
}

func (r *fileRotator) Write(data []byte) (n int, err error) {
	return r.logger.Write(data)
}

func (r *fileRotator) Close() error {
	return r.logger.Close()
}
