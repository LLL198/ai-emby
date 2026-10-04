package main

import (
	"context"
	"encoding/binary"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const sourceMappingTimeout = time.Second

func containerHostGateway() string {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return "127.0.0.1"
	}
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "127.0.0.1"
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "00000000" {
			continue
		}
		gateway, err := strconv.ParseUint(fields[2], 16, 32)
		if err == nil && gateway != 0 {
			ip := make(net.IP, 4)
			binary.LittleEndian.PutUint32(ip, uint32(gateway))
			return ip.String()
		}
	}
	return "127.0.0.1"
}

func localMappedSource(ctx context.Context, source string) (string, string) {
	dialer := &net.Dialer{Timeout: sourceMappingTimeout}
	return localMappedSourceWith(ctx, source, containerHostGateway(), net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

func localMappedSourceWith(ctx context.Context, source, gateway string, lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) (string, string) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()) != nil {
		return source, ""
	}
	address := net.ParseIP(gateway)
	if address == nil || address.IsLoopback() || lookup == nil || dial == nil {
		return source, ""
	}
	ctx, cancel := context.WithTimeout(ctx, sourceMappingTimeout)
	defer cancel()
	addresses, err := lookup(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return source, ""
	}
	for _, entry := range addresses {
		if !entry.IP.IsLoopback() && !(strings.EqualFold(parsed.Hostname(), "xiaoya.host") && (entry.IP.IsPrivate() || entry.IP.IsLinkLocalUnicast())) {
			return source, ""
		}
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	if connection, err := dial(ctx, "tcp", net.JoinHostPort(parsed.Hostname(), port)); err == nil {
		connection.Close()
		return source, ""
	}
	connection, err := dial(ctx, "tcp", net.JoinHostPort(gateway, port))
	if err != nil {
		return source, ""
	}
	connection.Close()
	originalHost := parsed.Host
	parsed.Host = net.JoinHostPort(gateway, port)
	return parsed.String(), originalHost
}
