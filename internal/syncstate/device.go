package syncstate

import (
	"encoding/json"
	"os"
	"path/filepath"

	"memodump/internal/cloudsync"
)

// deviceFile is the persisted device identity document. It is created once per
// installation, only when sync is first enabled, and is shared by every vault
// on the device.
type deviceFile struct {
	Version     int      `json:"version"`
	DeviceID    DeviceID `json:"deviceId"`
	DisplayName string   `json:"displayName"`
}

// loadDevice reads the device identity for an installation, creating a fresh
// one when absent or corrupt. A device ID is never stored inside a vault.
func loadDevice(stateRoot string) (DeviceID, error) {
	path := filepath.Join(stateRoot, "device.json")
	if data, err := os.ReadFile(path); err == nil {
		var d deviceFile
		if json.Unmarshal(data, &d) == nil &&
			d.Version == 1 && cloudsync.IsUUIDv4(string(d.DeviceID)) {
			return d.DeviceID, nil
		}
		// Corrupt device.json falls through to a fresh identity; the only
		// consequence is that future conflict names use a new device.
	}

	host, _ := os.Hostname()
	d := deviceFile{Version: 1, DeviceID: newDeviceID(), DisplayName: host}
	if err := writeFileDurable(path, &d); err != nil {
		return "", err
	}
	return d.DeviceID, nil
}
