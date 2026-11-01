package config

import "testing"

func TestValidateListener(t *testing.T) {
	tests := []struct {
		name    string
		address string
		key     string
		cert    string
		tlsKey  string
		wantErr bool
	}{
		{name: "default loopback", address: "127.0.0.1:26990"},
		{name: "loopback TLS pair", address: "[::1]:26990", cert: "cert.pem", tlsKey: "key.pem"},
		{name: "external requires key", address: "0.0.0.0:26990", wantErr: true},
		{name: "external requires TLS", address: "0.0.0.0:26990", key: "secret", wantErr: true},
		{name: "external configured", address: "0.0.0.0:26990", key: "secret", cert: "cert.pem", tlsKey: "key.pem"},
		{name: "partial TLS pair", address: "127.0.0.1:26990", cert: "cert.pem", wantErr: true},
		{name: "invalid address", address: "localhost", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateListener(tt.address, tt.key, tt.cert, tt.tlsKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateListener() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
