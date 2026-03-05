//go:build !linux

package main

import "fmt"

type unsupportedManager struct{}

// NewServiceManager returns an error-only stub on unsupported platforms.
func NewServiceManager(name string, system bool) ServiceManager {
	return &unsupportedManager{}
}

func (m *unsupportedManager) Install(opts ServiceOpts) error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Uninstall() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Enable() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Disable() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Status() (*ServiceStatus, error) {
	return nil, fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Logs(follow bool, lines int) error {
	return fmt.Errorf("service management is not supported on this platform")
}
