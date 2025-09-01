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
	LogLevel string `default:"Info"`
	LogFile  string

	// Blacklist for tracing and metrics
	Blacklist []string

	// APIServer addr
	APIServer struct {
		TCPAddr string `default:":19704"`
	}

	// HuaTuo config
	HuaTuoConf struct {
		UserName         string
		PassWord         string
		UnixAddr         string
		ServerIP         string
		APIVersion       string
		ReqTimeout       int
		OnlyOneSession   bool `default:"true"`
		KeepaliveEnable  bool `default:"true"`
		KeepaliveTimeout int
	}

	// RuntimeCgroup for huatuo-bamai resource
	RuntimeCgroup struct {
		// limit cpu num 0.5 2.0
		// limit memory (MB)
		LimitInitCPU float64 `default:"0.5"`
		LimitCPU     float64 `default:"2.0"`
		LimitMem     int64   `default:"2048"`
	}

	// Storage for huatuo-bamai tracer storage
	Storage struct {
		// ES configurations
		ES struct {
			Address, Username, Password, Index string
		}

		// LocalFile record file configuration
		LocalFile struct {
			Path         string `default:"record"`
			RotationSize int    `default:"100"`
			MaxRotation  int    `default:"10"`
		}
	}

	TaskConfig struct {
		MaxRunningTask int `default:"10"`
	}

	Tracing struct {
		// CPUIdle for cpuidle configuration
		CPUIdle struct {
			UserThreshold          int64
			SysThreshold           int64
			UsageThreshold         int64
			DeltaUserThreshold     int64
			DeltaSysThreshold      int64
			DeltaUsageThreshold    int64
			Interval               int64
			IntervalContinuousPerf int64
			PerfRunTimeOut         int64
		}

		// CPUSys for cpusys configuration
		CPUSys struct {
			SysThreshold      int64
			DeltaSysThreshold int64
			Interval          int64
			PerfRunTimeOut    int64
		}

		// Waitrate for waitrate.go
		Waitrate struct {
			SpikeThreshold map[string]float64
			SlopeThreshold map[string]float64
			SampleConfig   map[string]int
		}

		// Softirq for softirq thresh configuration
		Softirq struct {
			ThresholdTime uint64
		}

		// Dload for dload thresh configuration
		Dload struct {
			ThresholdLoad float64
			MonitorGap    int
		}

		// IOTracing for iotracer thresh configuration
		IOTracing struct {
			IOScheduleThreshold uint64
			ReadThreshold       uint64
			WriteThreshold      uint64
			IOutilThreshold     uint64
			IOwaitThreshold     uint64
			PeriodSecond        uint64
			MaxStackNumber      int
			TopProcessCount     int
			TopFilesPerProcess  int
		}

		// MemoryReclaim for MemoryReclaim configuration
		MemoryReclaim struct {
			Deltath uint64
		}

		// MemoryBurst configuration
		MemoryBurst struct {
			HistoryWindowLength int
			SampleInterval      int
			SilencePeriod       int
			TopNProcesses       int
			BurstRatio          float64
			AnonThreshold       int
		}

		// NetRecvLat configuration
		NetRecvLat struct {
			ToNetIf              uint64
			ToTCPV4              uint64
			ToUserCopy           uint64
			IgnoreHost           bool
			IgnoreContainerLevel []int
		}

		// Dropwatch configuration
		Dropwatch struct {
			IgnoreNeighInvalidate bool
		}

		// Netdev configuration
		Netdev struct {
			Whitelist []string
		}
		Fastfork struct {
			RedisInfoCollectionInterval uint32 `default:"3600"`
			EnableForkProbe             uint32 `default:"1"`
			EnablePtsepProbe            uint32 `default:"1"`
			EnableWaitptsepProbe        uint32 `default:"1"`
		}
	}

	MetricCollector struct {
		Netdev struct {
			// Use `netlink` instead of `procfs net/dev` to get netdev statistic.
			// Only support the host environment to use `netlink` now!
			EnableNetlink bool
			// IgnoredDevices: Ignore special devices in this netdev statistic.
			// AcceptDevices: Accept special devices in this netdev statistic.
			// These configurations use `Regexp`.
			// 'IgnoredDevices' has higher priority than 'AcceptDevices'.
			IgnoredDevices, AcceptDevices string
		}
		Qdisc struct {
			// IgnoredDevices: Ignore special devices in this qdisc statistic.
			// AcceptDevices: Accept special devices in this qdisc statistic.
			// These configurations use `Regexp`.
			// 'IgnoredDevices' has higher priority than 'AcceptDevices'.
			IgnoredDevices, AcceptDevices string
		}
		Vmstat struct {
			IncludedMetrics, ExcludedMetrics string
		}
		MemoryStat struct {
			IncludedMetrics, ExcludedMetrics string
		}
		MemoryEvents struct {
			IncludedMetrics, ExcludedMetrics string
		}
		Netstat struct {
			// ExcludedMetrics: Ignore keys in this netstat statistic.
			// IncludedMetrics: Accept keys in this netstat statistic.
			// The 'key' format: protocol + '_' + netstat_name. eg: TcpExt_TCPSynRetrans.
			// These configurations use `Regexp`.
			// 'ExcludedMetrics' has higher priority than 'IncludedMetrics'.
			ExcludedMetrics, IncludedMetrics string
		}
		MountPointStat struct {
			IncludedMountPoints string
		}
	}

	// WarningFilter for filt the known issues
	WarningFilter struct {
		PatternList [][]string
	}

	// Pod configuration
	Pod struct {
		KubeletPodListURL        string `default:"http://127.0.0.1:10255/pods"`
		KubeletPodListHTTPSURL   string `default:"https://127.0.0.1:10250/pods"`
		KubeletPodClientCertPath string `default:"/var/lib/kubelet/pki/kubelet-client-current.pem"`
		DockerAPIVersion         string `default:"1.24"`
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
