// Package raspapconf reads RaspAP's own configuration files. airhatchd keeps
// no copy of their settings; it reads them again whenever it needs them.
package raspapconf

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strings"
)

// Hostapd holds the hostapd.conf keys airhatchd checks.
type Hostapd struct {
	Interface  string
	SSID       string
	Passphrase string
	WPA        string
}

// ReadHostapd reads /etc/hostapd/hostapd.conf.
func ReadHostapd(path string) (Hostapd, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Hostapd{}, err
	}
	var h Hostapd
	fields := map[string]*string{"interface": &h.Interface, "ssid": &h.SSID, "wpa_passphrase": &h.Passphrase, "wpa": &h.WPA}
	for _, line := range lines(data) {
		key, value, ok := strings.Cut(line, "=")
		if field, known := fields[key]; ok && known {
			*field = value
		}
	}
	return h, nil
}

// HostapdIni holds the RaspAP topology switches of /etc/raspap/hostapd.ini.
type HostapdIni struct {
	BridgedEnable bool
	WifiAPEnable  bool
}

// ReadHostapdIni reads the optional hostapd.ini; a missing file is all off.
func ReadHostapdIni(path string) (HostapdIni, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return HostapdIni{}, nil
	}
	if err != nil {
		return HostapdIni{}, err
	}
	var ini HostapdIni
	for _, line := range lines(data) {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		on := strings.TrimSpace(value) == "1"
		switch strings.TrimSpace(key) {
		case "BridgedEnable":
			ini.BridgedEnable = on
		case "WifiAPEnable":
			ini.WifiAPEnable = on
		}
	}
	return ini, nil
}

// lines returns the file's lines without line endings.
func lines(data []byte) []string {
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	return out
}
