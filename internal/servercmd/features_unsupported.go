//go:build !linux && !windows && !darwin

package servercmd

import (
	"fmt"

	"headlessdesk/internal/desktop"
)

func supportedBackendTypesDescription() string {
	return "rdp, vnc, command, or nanokvm"
}

func validateBackendPlatform(name string, backendType string) error {
	switch backendType {
	case "kwin", "eis":
		return fmt.Errorf("backends.%s.type %s is only supported on linux", name, backendType)
	case "windows":
		return fmt.Errorf("backends.%s.type windows is only supported on windows", name)
	case "macos":
		return fmt.Errorf("backends.%s.type macos is only supported on macos", name)
	default:
		return nil
	}
}

func startKWinBackend(name string) (desktop.OutputBackend, error) {
	return nil, fmt.Errorf("backend %q type kwin is only supported on linux", name)
}

func startKWinEISBackend(name string) (desktop.InputBackend, error) {
	return nil, fmt.Errorf("backend %q type eis is only supported on linux", name)
}

func startWindowsBackend(name string) (desktop.Session, error) {
	return nil, fmt.Errorf("backend %q type windows is only supported on windows", name)
}

func startMacOSBackend(name string) (desktop.Session, error) {
	return nil, fmt.Errorf("backend %q type macos is only supported on macos", name)
}
