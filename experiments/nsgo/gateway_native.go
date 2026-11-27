//go:build !js

package main

import (
	"net/http/cookiejar"
	"os"
)

func init() {
	// Keep the native login cookie in memory. Browsers manage their own cookie jar.
	chatClient.Jar, _ = cookiejar.New(nil)
}

func apiURL(path string) string {
	if os.Getenv("DEX_NSGO_POC_TEST") == "1" {
		if base := os.Getenv("DEX_NSGO_TEST_URL"); base != "" {
			return base + path
		}
	}
	return chatGateway + path
}
