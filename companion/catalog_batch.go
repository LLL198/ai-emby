package main

import "strings"

func catalogPlaceholders(count int) string {
	if count < 1 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
