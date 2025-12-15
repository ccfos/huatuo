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

package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sirupsen/logrus"
)

var logger *logrus.Logger

const rfc3339NanoFixed = "2006-01-02T15:04:05.000000000Z07:00"

func init() {
	logger = logrus.New()

	logger.SetFormatter(&logrus.TextFormatter{
		DisableColors:   true,
		ForceQuote:      true,
		FullTimestamp:   true,
		TimestampFormat: rfc3339NanoFixed,
		DisableSorting:  false,
	})

	logger.SetOutput(os.Stdout)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetReportCaller(false)
}

func newLogrusEntry(callerSkip int) *logrus.Entry {
	var function string

	pc, file, line, ok := runtime.Caller(callerSkip)
	if !ok {
		file = "<???>"
		function = "<???>"
		line = 1
	} else {
		file = filepath.Base(file)
		function = runtime.FuncForPC(pc).Name()
	}

	return logger.WithFields(logrus.Fields{
		logrus.FieldKeyFunc: function,
		logrus.FieldKeyFile: fmt.Sprintf("%s:%d", file, line),
	})
}

// SetLevel aims to set the log level
func SetLevel(lvl string) {
	level, err := logrus.ParseLevel(lvl)
	if err != nil {
		Errorf("invalid lvl: %v", err)
		return
	}

	logger.SetLevel(level)
}

// GetLevel returns the standard logger level.
func GetLevel() logrus.Level {
	return logger.GetLevel()
}

// SetOutput sets the standard logger output.
func SetOutput(out io.Writer) {
	logger.SetOutput(out)
}

// AddHook adds a hook to the standard logger hooks.
func AddHook(hook logrus.Hook) {
	logger.AddHook(hook)
}

// WithError creates an entry from the standard logger and adds an error to it, using the value defined in ErrorKey as key.
func WithError(err error) *logrus.Entry {
	return newLogrusEntry(2).WithError(err)
}

// Debug logs a message at level Debug on the standard logger.
func Debug(args ...any) {
	if logger.IsLevelEnabled(logrus.DebugLevel) {
		newLogrusEntry(2).Debug(args...)
	}
}

// Info logs a message at level Info on the standard logger.
func Info(args ...any) {
	if logger.IsLevelEnabled(logrus.InfoLevel) {
		newLogrusEntry(2).Info(args...)
	}
}

// Warn logs a message at level Warn on the standard logger.
func Warn(args ...any) {
	if logger.IsLevelEnabled(logrus.WarnLevel) {
		newLogrusEntry(2).Warn(args...)
	}
}

// Error logs a message at level Error on the standard logger.
func Error(args ...any) {
	if logger.IsLevelEnabled(logrus.ErrorLevel) {
		newLogrusEntry(2).Error(args...)
	}
}

// Panic logs a message at level Panic on the standard logger.
func Panic(args ...any) {
	if logger.IsLevelEnabled(logrus.PanicLevel) {
		newLogrusEntry(2).Panic(args...)
	}
}

// Fatal logs a message at level Fatal on the standard logger then the process will exit with status set to 1.
func Fatal(args ...any) {
	if logger.IsLevelEnabled(logrus.FatalLevel) {
		newLogrusEntry(2).Fatal(args...)
	}
}

// Debugf logs a message at level Debug on the standard logger.
func Debugf(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.DebugLevel) {
		newLogrusEntry(2).Debugf(format, args...)
	}
}

// Infof logs a message at level Info on the standard logger.
func Infof(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.InfoLevel) {
		newLogrusEntry(2).Infof(format, args...)
	}
}

// Warnf logs a message at level Warn on the standard logger.
func Warnf(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.WarnLevel) {
		newLogrusEntry(2).Warnf(format, args...)
	}
}

// Errorf logs a message at level Error on the standard logger.
func Errorf(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.ErrorLevel) {
		newLogrusEntry(2).Errorf(format, args...)
	}
}

// Panicf logs a message at level Panic on the standard logger.
func Panicf(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.PanicLevel) {
		newLogrusEntry(2).Panicf(format, args...)
	}
}

// Fatalf logs a message at level Fatal on the standard logger then the process will exit with status set to 1.
func Fatalf(format string, args ...any) {
	if logger.IsLevelEnabled(logrus.FatalLevel) {
		newLogrusEntry(2).Fatalf(format, args...)
	}
}

// WithCallerSkip creates an entry from the caller skip.
func WithCallerSkip(skip int) *logrus.Entry {
	return newLogrusEntry(2 + skip)
}
