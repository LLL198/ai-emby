package main

import (
	"fmt"
	"math/big"
	"strings"
)

const (
	playbackIDHexLength     = 32
	playbackIDDecimalLength = 40
	playbackIDBits          = 128
)

// Some players require decimal IDs. The leading 9 identifies this reversible
// representation of the 128-bit hexadecimal catalog ID.
func numericPlaybackID(value string) string {
	if len(value) != playbackIDHexLength || strings.Trim(value, "0123456789abcdef") != "" {
		return value
	}
	number, ok := new(big.Int).SetString(value, 16)
	if !ok {
		return value
	}
	return "9" + fmt.Sprintf("%039s", number.String())
}

func canonicalPlaybackID(value string) string {
	if len(value) != playbackIDDecimalLength || value[0] != '9' || strings.Trim(value, "0123456789") != "" {
		return value
	}
	number, ok := new(big.Int).SetString(value[1:], 10)
	if !ok || number.BitLen() > playbackIDBits {
		return value
	}
	return fmt.Sprintf("%032x", number)
}
