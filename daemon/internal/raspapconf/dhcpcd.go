package raspapconf

import (
	"net/netip"
	"strings"
)

// SplitDhcpcd splits dhcpcd.conf into the global section and blocks. A block
// starts at a line whose first word is interface, profile or ssid and runs
// to the next such line.
func SplitDhcpcd(conf []byte) (global []string, blocks [][]string) {
	for _, line := range lines(conf) {
		fields := strings.Fields(line)
		if len(fields) > 0 && (fields[0] == "interface" || fields[0] == "profile" || fields[0] == "ssid") {
			blocks = append(blocks, []string{line})
			continue
		}
		if len(blocks) == 0 {
			global = append(global, line)
		} else {
			blocks[len(blocks)-1] = append(blocks[len(blocks)-1], line)
		}
	}
	return global, blocks
}

// IsWlan0 reports whether block is an "interface wlan0" block.
func IsWlan0(block []string) bool {
	fields := strings.Fields(block[0])
	return len(fields) == 2 && fields[0] == "interface" && fields[1] == "wlan0"
}

// APAddress is the static ip_address of the last interface wlan0 block that
// has one, as dhcpcd applies matching blocks in order.
func APAddress(conf []byte) (netip.Prefix, bool) {
	var found netip.Prefix
	_, blocks := SplitDhcpcd(conf)
	for _, block := range blocks {
		if !IsWlan0(block) {
			continue
		}
		for _, line := range block[1:] {
			value, ok := strings.CutPrefix(strings.TrimSpace(line), "static ip_address=")
			if !ok {
				continue
			}
			if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Addr().Is4() {
				found = prefix
			}
		}
	}
	return found, found.IsValid()
}
