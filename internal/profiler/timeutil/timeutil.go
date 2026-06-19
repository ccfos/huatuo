package timeutil

import "time"

func ParseWithFallback(raw, layout string, fallback time.Time) time.Time {
	if raw == "" {
		return fallback.UTC()
	}

	parsed, err := time.Parse(layout, raw)
	if err == nil {
		return parsed.UTC()
	}

	return fallback.UTC()
}
