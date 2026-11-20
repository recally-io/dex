package main

import (
	"testing"

	"github.com/recally-io/dex/internal/service"
)

func TestMachineListenDefaultsToTheNextPort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config service.Config
		want   string
	}{
		{"loopback stays off", service.Config{Host: "127.0.0.1", Port: 8787}, ""},
		{"public host gets the next port", service.Config{Host: "0.0.0.0", Port: 8787}, "0.0.0.0:8788"},
		{"explicit address wins", service.Config{Host: "0.0.0.0", Port: 8787, MachineListen: "0.0.0.0:9000"}, "0.0.0.0:9000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := machineListen(tc.config)
			if err != nil || got != tc.want {
				t.Fatalf("machineListen(%+v) = %q, %v; want %q", tc.config, got, err, tc.want)
			}
		})
	}
}
