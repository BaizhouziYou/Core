package config

import (
	"fmt"
	"net"
	"strings"
)

func ValidateListener(address, apiKey, certFile, keyFile string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("EASYBOT_TLS_CERT and EASYBOT_TLS_KEY must be configured together")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback {
		if apiKey == "" {
			return fmt.Errorf("EASYBOT_API_KEY is required when listening beyond loopback")
		}
		if certFile == "" {
			return fmt.Errorf("EASYBOT_TLS_CERT and EASYBOT_TLS_KEY are required when listening beyond loopback")
		}
	}
	return nil
}
