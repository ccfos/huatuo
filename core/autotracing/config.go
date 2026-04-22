package autotracing

// Config holds autotracing configuration used by the package at runtime.
type Config struct {
	CPUIdle struct {
		UserThreshold         int64 `default:"75"`
		SysThreshold          int64 `default:"45"`
		UsageThreshold        int64 `default:"90"`
		DeltaUserThreshold    int64 `default:"45"`
		DeltaSysThreshold     int64 `default:"20"`
		DeltaUsageThreshold   int64 `default:"55"`
		Interval              int64 `default:"10"`
		IntervalTracing       int64 `default:"1800"`
		RunTracingToolTimeout int64 `default:"10"`
	}

	CPUSys struct {
		SysThreshold          int64 `default:"45"`
		DeltaSysThreshold     int64 `default:"20"`
		Interval              int64 `default:"10"`
		RunTracingToolTimeout int64 `default:"10"`
	}

	Dload struct {
		ThresholdLoad   int64 `default:"5"`
		Interval        int64 `default:"10"`
		IntervalTracing int64 `default:"1800"`
	}

	IOTracing struct {
		RbpsThreshold         uint64 `default:"2000"`
		WbpsThreshold         uint64 `default:"1500"`
		UtilThreshold         uint64 `default:"90"`
		AwaitThreshold        uint64 `default:"100"`
		RunTracingToolTimeout uint64 `default:"10"`
		MaxProcDump           int    `default:"10"`
		MaxFilesPerProcDump   int    `default:"5"`
	}

	MemoryBurst struct {
		DeltaMemoryBurst    int `default:"100"`
		DeltaAnonThreshold  int `default:"70"`
		Interval            int `default:"10"`
		IntervalTracing     int `default:"1800"`
		SlidingWindowLength int `default:"60"`
		DumpProcessMaxNum   int `default:"10"`
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
