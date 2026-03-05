package main

import (
	"testing"
	"time"
)

func TestMDNSAdvertiseAndDiscover(t *testing.T) {
	name := "test-brain"
	serviceType := "_thought-store-test._tcp"
	port := 9999

	// Advertise
	server, err := AdvertiseService(name, serviceType, port)
	if err != nil {
		t.Skip("mDNS not available (multicast may be disabled):", err)
	}
	defer server.Shutdown()

	// Discover by name
	urls, err := DiscoverByName(name, serviceType, 2*time.Second)
	if err != nil {
		t.Fatalf("DiscoverByName: %v", err)
	}
	if len(urls) == 0 {
		t.Fatal("expected at least one discovered URL")
	}

	// Verify URL format
	for _, u := range urls {
		if u == "" {
			t.Fatal("empty URL in discovered results")
		}
		t.Logf("Discovered: %s", u)
	}

	// ScanAll should also find it
	services, err := ScanAll(serviceType, 2*time.Second)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}

	found := false
	for _, svc := range services {
		if svc.Name == name && svc.Port == port {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ScanAll did not find %q among %d services", name, len(services))
	}
}

func TestParsePort(t *testing.T) {
	tests := []struct {
		addr string
		port int
		err  bool
	}{
		{":3000", 3000, false},
		{"0.0.0.0:8080", 8080, false},
		{"localhost:443", 443, false},
		{"bad", 0, true},
	}
	for _, tt := range tests {
		port, err := ParsePort(tt.addr)
		if tt.err {
			if err == nil {
				t.Errorf("ParsePort(%q) expected error", tt.addr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePort(%q) unexpected error: %v", tt.addr, err)
			continue
		}
		if port != tt.port {
			t.Errorf("ParsePort(%q) = %d, want %d", tt.addr, port, tt.port)
		}
	}
}
