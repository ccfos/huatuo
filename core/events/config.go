package events

// Config holds event tracing configuration used by the package at runtime.
type Config struct {
	Softirq struct {
		DisabledThreshold uint64 `default:"10000000"`
	}

	MemoryReclaim struct {
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

	PatternList [][]string
}

var cfg = &Config{}

// SetConfig updates the package level config.
func SetConfig(c *Config) {
	if c == nil {
		cfg = &Config{}
		return
	}
	cfg = c
}
