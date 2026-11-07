package config

import (
	"fmt"
	"net"
	"strings"
)

func IsLoopbackListener(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") || (ip != nil && ip.IsLoopback())
}

func ValidateListener(address, certFile, keyFile string) error {
	_, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("监听地址无效: %w", err)
	}
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("TLS 证书和私钥必须成对配置；使用 HTTP 时可同时留空")
	}
	return nil
}
