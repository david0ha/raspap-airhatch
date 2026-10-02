package raspapconf

import (
	"crypto/sha256"
	"errors"
	"os"
	"strings"
)

// Network is one network={…} block of wpa_supplicant.conf. SSID is decoded
// and empty when the block has none. Digest covers the block's body.
type Network struct {
	SSID     string
	Disabled bool
	Digest   [sha256.Size]byte
}

// Wpa is RaspAP's /etc/wpa_supplicant/wpa_supplicant.conf.
type Wpa struct {
	CtrlInterface string
	Networks      []Network
}

// ReadWpa reads wpa_supplicant.conf; a missing file has no networks.
func ReadWpa(path string) (Wpa, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Wpa{}, nil
	}
	if err != nil {
		return Wpa{}, err
	}
	return parseWpa(data), nil
}

func parseWpa(data []byte) Wpa {
	var w Wpa
	var body []string
	inBlock := false
	for _, raw := range lines(data) {
		line := strings.TrimSpace(raw)
		switch {
		case !inBlock && line == "network={":
			inBlock, body = true, nil
		case inBlock && line == "}":
			w.Networks = append(w.Networks, parseNetwork(body))
			inBlock = false
		case inBlock:
			if line != "" && !strings.HasPrefix(line, "#") {
				body = append(body, line)
			}
		case strings.HasPrefix(line, "ctrl_interface="):
			w.CtrlInterface = strings.TrimPrefix(line, "ctrl_interface=")
		}
	}
	return w
}

// parseNetwork digests the body lines (trimmed, comments such as
// wpa_passphrase's #psk= dropped).
func parseNetwork(body []string) Network {
	n := Network{Digest: sha256.Sum256([]byte(strings.Join(body, "\n")))}
	for _, line := range body {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ssid":
			n.SSID, _ = DecodeSSID(value)
		case "disabled":
			n.Disabled = value == "1"
		}
	}
	return n
}

// ControlDir is the directory of ctrl_interface=, in either its plain or
// its DIR=… GROUP=… form, or "" when the line is missing.
func (w Wpa) ControlDir() string {
	value := w.CtrlInterface
	if rest, ok := strings.CutPrefix(value, "DIR="); ok {
		value, _, _ = strings.Cut(rest, " ")
	}
	return value
}

// Saved counts the networks the supplicant would try: an SSID and not disabled.
func Saved(networks []Network) int {
	n := 0
	for _, network := range networks {
		if usable(network) {
			n++
		}
	}
	return n
}

func usable(n Network) bool { return n.SSID != "" && !n.Disabled }

// Changed reports whether cur has a usable network whose SSID or body is not
// in prev, and returns the first such SSID. Deleting networks is no change.
func Changed(prev, cur []Network) (bool, string) {
	known := map[Network]bool{}
	for _, n := range prev {
		known[n] = true
	}
	for _, n := range cur {
		if usable(n) && !known[n] {
			return true, n.SSID
		}
	}
	return false, ""
}
