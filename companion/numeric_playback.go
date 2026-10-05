package main

import (
	"fmt"
	"math/big"
	"strings"
)

func numericPlaybackID(value string) string {
	if len(value) != 32 || strings.Trim(value, "0123456789abcdef") != "" {
		return value
	}
	number, ok := new(big.Int).SetString(value, 16)
	if !ok {
		return value
	}
	return "9" + fmt.Sprintf("%039s", number.String())
}

func canonicalPlaybackID(value string) string {
	if len(value) != 40 || value[0] != '9' || strings.Trim(value, "0123456789") != "" {
		return value
	}
	number, ok := new(big.Int).SetString(value[1:], 10)
	if !ok || number.BitLen() > 128 {
		return value
	}
	return fmt.Sprintf("%032x", number)
}
