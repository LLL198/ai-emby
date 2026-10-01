package main

import "strings"

// 0x77a640. Remaining catalog batch functions are still being recovered.
func catalogPlaceholders(count int) string {
	if count < 1 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
