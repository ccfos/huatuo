package strutil

import "strings"

func SplitCommaList(raw string) []string {
	if raw == "" {
		return nil
	}

	var parts []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}

	return parts
}
