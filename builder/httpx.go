package main

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// detectedProxy is set once at startup by resolveProxy().
var detectedProxy string

// noMirrorFallback disables the automatic use of public mirrors (--no-mirror).
var noMirrorFallback bool

// newHTTPClient returns an HTTP client that honours, in order:
//   - the proxy detected by resolveProxy() (flag / env / Windows settings)
//   - the standard http_proxy / https_proxy environment variables
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if detectedProxy != "" {
		if parsed, err := url.Parse(detectedProxy); err == nil && parsed.Host != "" {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}
