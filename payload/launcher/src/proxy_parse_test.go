package main

import (
	"strings"
	"testing"
)

func TestParseProxyValuePlain(t *testing.T) {
	got := parseProxyValue("127.0.0.1:7897")
	if got["all"] != "http://127.0.0.1:7897" {
		t.Fatalf("plain host:port => %q", got["all"])
	}
}

func TestParseProxyValueWithScheme(t *testing.T) {
	got := parseProxyValue("http://1.2.3.4:8080")
	if got["all"] != "http://1.2.3.4:8080" {
		t.Fatalf("scheme preserved => %q", got["all"])
	}
}

func TestParseProxyValuePerProtocol(t *testing.T) {
	got := parseProxyValue("http=127.0.0.1:7897;https=127.0.0.1:7897")
	if got["http"] != "http://127.0.0.1:7897" || got["https"] != "http://127.0.0.1:7897" {
		t.Fatalf("per-protocol parse => %#v", got)
	}
}

func TestParseProxyValueSocks(t *testing.T) {
	got := parseProxyValue("socks=127.0.0.1:1080")
	if got["socks"] != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks => %#v", got)
	}
}

func TestParseProxyValueRealWorld(t *testing.T) {
	// exactly what Clash / v2rayN write into the Windows settings
	got := parseProxyValue("http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891")
	if got["http"] != "http://127.0.0.1:7890" ||
		got["https"] != "http://127.0.0.1:7890" ||
		got["socks"] != "socks5://127.0.0.1:7891" {
		t.Fatalf("clash style => %#v", got)
	}
}

func TestParseProxyValueEmpty(t *testing.T) {
	if len(parseProxyValue("")) != 0 {
		t.Fatal("empty input must produce no entries")
	}
}

func TestPickOrder(t *testing.T) {
	if pick("", "", "socks5://x") != "socks5://x" {
		t.Fatal("pick must return the first non-empty value")
	}
	if pick("a", "b") != "a" {
		t.Fatal("pick must prefer the first value")
	}
}

func TestMergeBypassAlwaysLocal(t *testing.T) {
	got := mergeBypass("", "")
	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bypass list must contain %s: %q", want, got)
		}
	}
}

func TestMergeBypassCombinesAndDeduplicates(t *testing.T) {
	got := mergeBypass("example.com,127.0.0.1", "<local>;*.internal;example.com")
	for _, want := range []string{"example.com", "<local>", "*.internal"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	if strings.Count(got, "example.com") != 1 {
		t.Fatalf("duplicates not removed: %q", got)
	}
}
