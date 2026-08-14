//go:build darwin

package servercmd

import (
	"fmt"

	"headlessdesk/internal/desktop"
	"headlessdesk/internal/macoslocal"
)

func supportedBackendTypesDescription() string {
	return "rdp, vnc, command, nanokvm, or macos"
}

func validateBackendPlatform(name string, backendType string) error {
	switch backendType {
	case "kwin", "eis":
		return fmt.Errorf("backends.%s.type %s is only supported on linux", name, backendType)
	case "windows":
		return fmt.Errorf("backends.%s.type windows is only supported on windows", name)
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
	backend, err := macoslocal.New()
	if err != nil {
		return nil, fmt.Errorf("start macOS backend %q: %w", name, err)
	}
	return backend, nil
}
