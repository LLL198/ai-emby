package main

import "strings"

// catalogLike escapes SQL LIKE metacharacters for literal catalog searches.
func catalogLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
