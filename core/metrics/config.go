package collector

// Config holds metric collector configuration used by the package at runtime.
type Config struct {
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
		IncludedOnHost      string
		ExcludedOnHost      string
		IncludedOnContainer string
		ExcludedOnContainer string
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

var cfg = &Config{}

// SetConfig updates the package level config.
func SetConfig(c *Config) {
	if c == nil {
		cfg = &Config{}
		return
	}
	cfg = c
}
