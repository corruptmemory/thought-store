package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

// AdvertiseService starts an mDNS server advertising the given service.
// Returns the server (call Shutdown() to stop) or an error.
func AdvertiseService(name, serviceType string, port int) (*mdns.Server, error) {
	host, _ := os.Hostname()
	info := []string{"path=/api"}

	service, err := mdns.NewMDNSService(name, serviceType, "", host+".", port, nil, info)
	if err != nil {
		return nil, fmt.Errorf("create mdns service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("start mdns server: %w", err)
	}

	return server, nil
}

// DiscoveredService holds info about a discovered mDNS service instance.
type DiscoveredService struct {
	Name string
	Host string
	Port int
	Addr net.IP
	Info []string
}

// URL returns the HTTP URL for this discovered service.
func (d DiscoveredService) URL() string {
	host := d.Addr.String()
	if d.Addr.To4() == nil {
		host = "[" + host + "]" // IPv6
	}
	return fmt.Sprintf("http://%s:%d", host, d.Port)
}

// DiscoverByName finds a specific named instance of a service type.
// Returns URLs for all addresses of the matching instance.
func DiscoverByName(name, serviceType string, timeout time.Duration) ([]string, error) {
	entriesCh := make(chan *mdns.ServiceEntry, 8)

	var urls []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entriesCh {
			if entry.Name == name+"."+serviceType+".local." || entry.Name == name {
				if entry.AddrV4 != nil {
					urls = append(urls, fmt.Sprintf("http://%s:%d", entry.AddrV4, entry.Port))
				}
				if entry.AddrV6 != nil {
					urls = append(urls, fmt.Sprintf("http://[%s]:%d", entry.AddrV6, entry.Port))
				}
			}
		}
	}()

	params := mdns.DefaultParams(serviceType)
	params.Entries = entriesCh
	params.Timeout = timeout

	err := mdns.Query(params)
	close(entriesCh)
	<-done

	if err != nil {
		return nil, fmt.Errorf("mdns query: %w", err)
	}

	return urls, nil
}

// ScanAll finds all instances of a service type on the local network.
func ScanAll(serviceType string, timeout time.Duration) ([]DiscoveredService, error) {
	entriesCh := make(chan *mdns.ServiceEntry, 16)

	var services []DiscoveredService
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entriesCh {
			svc := DiscoveredService{
				Name: instanceName(entry.Name),
				Host: entry.Host,
				Port: entry.Port,
				Info: entry.InfoFields,
			}
			if entry.AddrV4 != nil {
				svc.Addr = entry.AddrV4
			} else if entry.AddrV6 != nil {
				svc.Addr = entry.AddrV6
			}
			services = append(services, svc)
		}
	}()

	params := mdns.DefaultParams(serviceType)
	params.Entries = entriesCh
	params.Timeout = timeout

	err := mdns.Query(params)
	close(entriesCh)
	<-done

	if err != nil {
		return nil, fmt.Errorf("mdns scan: %w", err)
	}

	return services, nil
}

// instanceName extracts the instance name from a fully qualified mDNS name.
// e.g., "jim-brain._thought-store._tcp.local." → "jim-brain"
func instanceName(fullName string) string {
	if i := strings.Index(fullName, "."); i > 0 {
		return fullName[:i]
	}
	return fullName
}

// ParsePort extracts the port number from a listen address like ":3000" or "0.0.0.0:3000".
func ParsePort(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}
	return port, nil
}
