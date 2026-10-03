package netguard

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestIsPrivate(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:192.168.0.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"::ffff:8.8.8.8", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.ip)
			if got := IsPrivate(addr); got != tc.private {
				t.Errorf("IsPrivate(%s) = %v; want %v", tc.ip, got, tc.private)
			}
		})
	}
}

func TestCheckURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	badURLs := []string{
		"http://127.0.0.1/test",
		"http://10.0.0.5/api",
		"http://192.168.1.100/data",
		"http://169.254.169.254/latest/meta-data/",
		"ftp://example.com/file.txt",
		"file:///etc/passwd",
		"invalid-url",
	}

	for _, u := range badURLs {
		t.Run("bad_"+u, func(t *testing.T) {
			if err := CheckURL(ctx, u); err == nil {
				t.Errorf("CheckURL(%q) expected error, got nil", u)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient(Options{Timeout: 5 * time.Second})
	if c.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", c.Timeout)
	}
	if c.Transport == nil {
		t.Errorf("expected non-nil transport")
	}
}
