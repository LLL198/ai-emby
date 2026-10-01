package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func serverListenAddress() (string, error) {
	value := strings.TrimSpace(os.Getenv("PORT"))
	if value == "" {
		value = "8097"
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("PORT must be an integer between 1 and 65535")
	}
	return ":" + strconv.Itoa(port), nil
}
