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

package conf

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"huatuo-bamai/internal/log"

	"github.com/pelletier/go-toml"
)

// CommonConf global common configuration
type CommonConf struct {
	Log struct {
		Level string `default:"Info"`
		File  string
	}

	// BlackList for tracing and metrics
	BlackList []string

	// huatuo-bamai server listen addr
	APIServer struct {
		TCPAddr string `default:":19704"`
	}

	// RuntimeCgroup for huatuo-bamai resource
	// limit cpu num 0.5 2.0
	// limit memory (MB)
	RuntimeCgroup struct {
		LimitInitCPU float64 `default:"0.5"`
		LimitCPU     float64 `default:"2.0"`
		LimitMem     int64   `default:"2048"`
	}

	// Storage for huatuo-bamai tracer storage
	Storage struct {
		ES struct {
			Address            string `default:"http://127.0.0.1:9200"`
			Username, Password string
			Index              string `default:"huatuo_bamai"`
		}

		LocalFile struct {
			Path         string `default:"huatuo-local"`
			RotationSize int    `default:"100"`
			MaxRotation  int    `default:"10"`
		}
	}

	// Will be exported, utill next version is ok.
	TaskConfig struct {
		MaxRunningTask int `default:"10"`
	}

	AutoTracing struct {
		CPUIdle struct {
			UserThreshold         int64 `default:"75"`
			SysThreshold          int64 `default:"45"`
			UsageThreshold        int64 `default:"90"`
			DeltaUserThreshold    int64 `default:"45"`
			DeltaSysThreshold     int64 `default:"20"`
			DeltaUsageThreshold   int64 `default:"55"`
			Interval              int64 `default:"10"`
			IntervalContinuousRun int64 `default:"1800"`
			PerfRunTimeOut        int64 `default:"10"`
		}

		CPUSys struct {
			SysThreshold      int64 `default:"45"`
			DeltaSysThreshold int64 `default:"20"`
			Interval          int64 `default:"10"`
			PerfRunTimeOut    int64 `default:"10"`
		}

		Dload struct {
			ThresholdLoad float64 `default:"5.0"`
			MonitorGap    int     `default:"180"`
		}

		IOTracing struct {
			RbpsThreshold       uint64 `default:"2000"`
			WbpsThreshold       uint64 `default:"1500"`
			UtilThreshold       uint64 `default:"90"`
			AwaitThreshold      uint64 `default:"100"`
			RunIOTracingTimeout uint64 `default:"10"`
			MaxProcDump         int    `default:"10"`
			MaxFilesPerProcDump int    `default:"5"`
		}

		MemoryBurst struct {
			DeltaMemoryBurst      int `default:"100"`
			DeltaAnonThreshold    int `default:"70"`
			Interval              int `default:"10"`
			IntervalContinuousRun int `default:"1800"`
			SlidingWindowLength   int `default:"60"`
			DumpProcessMaxNum     int `default:"10"`
		}
	}

	EventTracing struct {
		Softirq struct {
			// 10ms
			DisabledThreshold uint64 `default:"10000000"`
		}

		MemoryReclaim struct {
			// 900ms
			BlockedThreshold uint64 `default:"900000000"`
		}

		NetRxLatency struct {
			Driver2NetRx             uint64 `default:"5"`
			Driver2TCP               uint64 `default:"10"`
			Driver2Userspace         uint64 `default:"115"`
			ExcludedHostNetnamespace bool   `default:"true"`
			ExcludedContainerQos     []string
		}

		Dropwatch struct {
			ExcludedNeighInvalidate bool `default:"true"`
		}

		Netdev struct {
			DeviceList []string
		}
	}

	MetricCollector struct {
		NetdevStats struct {
			EnableNetlink  bool `default:"false"`
			DeviceExcluded string
			DeviceIncluded string
		}
		NetdevDCB struct {
			DeviceList []string
		}
		NetdevHW struct {
			DeviceList []string
		}
		Qdisc struct {
			DeviceExcluded string
			DeviceIncluded string
		}
		Vmstat struct {
			Included string
			Excluded string
		}
		MemoryStat struct {
			Included string
			Excluded string
		}
		MemoryEvents struct {
			Included string
			Excluded string
		}
		Netstat struct {
			Included string
			Excluded string
		}
		MountPointStat struct {
			MountPointsIncluded string
		}
	}

	// WarningFilter for filt the known issues
	WarningFilter struct {
		PatternList [][]string
	}

	// Pod configuration
	Pod struct {
		KubeletReadOnlyPort   uint32 `default:"10255"`
		KubeletAuthorizedPort uint32 `default:"10250"`
		KubeletClientCertPath string
		DockerAPIVersion      string `default:"1.24"`
	}
}

var (
	lock       = sync.Mutex{}
	configFile = ""
	config     = &CommonConf{}

	// Region is host and containers belong to.
	Region string
)

// LoadConfig load conf file
func LoadConfig(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// defaults.SetDefaults(config)
	d := toml.NewDecoder(f)
	if err := d.Strict(true).Decode(config); err != nil {
		return err
	}

	// MB
	config.RuntimeCgroup.LimitMem *= 1024 * 1024
	configFile = path

	log.Infof("Loadconfig:\n%+v\n", config)
	return nil
}

// Get return the global configuration obj
func Get() *CommonConf {
	return config
}

// Set is a function that modifies the configuration obj
//
//	 @key: supported keys
//			- "Key1"
//			- "Key1.Key2"
func Set(key string, val any) {
	lock.Lock()
	defer lock.Unlock()

	// find key
	c := reflect.ValueOf(config)
	for _, k := range strings.Split(key, ".") {
		elem := c.Elem().FieldByName(k)
		if !elem.IsValid() || !elem.CanAddr() {
			panic(fmt.Errorf("invalid elem %s: %v", key, elem))
		}
		c = elem.Addr()
	}

	// assign
	rc := reflect.Indirect(c)
	rval := reflect.ValueOf(val)
	if rc.Kind() != rval.Kind() {
		panic(fmt.Errorf("%s type %s is not assignable to type %s", key, rc.Kind(), rval.Kind()))
	}

	rc.Set(rval)
	log.Infof("Config: set %s = %v", key, val)
}

// Sync write config data to file
func Sync() error {
	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	return encoder.Encode(config)
}

// KnownIssueSearch search the known issue pattern in
// the stack and return pattern name if found.
func KnownIssueSearch(srcPattern, srcMatching1, srcMatching2 string) (issueName string, inKnownList uint64) {
	for _, p := range config.WarningFilter.PatternList {
		if len(p) < 2 {
			log.Infof("Invalid configuration, please check the config file!")
			return "", 0
		}

		rePattern := regexp.MustCompile(p[1])
		if rePattern.MatchString(srcPattern) {
			if srcMatching1 != "" && len(p) >= 3 && p[2] != "" {
				re1 := regexp.MustCompile(p[2])
				if re1.MatchString(srcMatching1) {
					return p[0], 1
				}
			}

			if srcMatching2 != "" && len(p) >= 4 && p[3] != "" {
				re2 := regexp.MustCompile(p[3])
				if re2.MatchString(srcMatching2) {
					return p[0], 1
				}
			}

			return p[0], 0
		}
	}
	return "", 0
}
