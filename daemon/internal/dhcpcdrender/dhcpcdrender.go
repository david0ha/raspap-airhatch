// Package dhcpcdrender renders dhcpcd's per-mode configuration from RaspAP's
// /etc/dhcpcd.conf (docs/API.md §6.3). Only wlan0 changes.
package dhcpcdrender

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/david0ha/raspap-airhatch/daemon/internal/atomicfile"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
)

// Mode is wlan0's mode as recorded in /run/airhatch/mode.
type Mode string

const (
	Client Mode = "client"
	AP     Mode = "ap"
)

// ReadMode returns AP only when the mode file says "ap"; anything else,
// including a missing file, is Client.
func ReadMode(path string) Mode {
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) == string(AP) {
		return AP
	}
	return Client
}

// Render never fails: a nil conf renders the header and wlan0's mode lines.
// Client mode drops every "interface wlan0" block and appends a DHCP client
// block for wlan0 without the wpa_supplicant hook (keeping its metric); AP
// mode drops them and denies wlan0 so airhatchd owns its address.
func Render(conf []byte, mode Mode) []byte {
	global, blocks := raspapconf.SplitDhcpcd(conf)
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Rendered by airhatchd render-dhcpcd from /etc/dhcpcd.conf (mode %s). Do not edit.\n", mode)
	writeLines(&out, global)
	if mode == AP {
		out.WriteString("denyinterfaces wlan0\n")
	}
	metric := ""
	for _, block := range blocks {
		if !raspapconf.IsWlan0(block) {
			writeLines(&out, block)
			continue
		}
		for _, line := range block[1:] {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "metric" {
				metric = line
			}
		}
	}
	if mode == Client {
		out.WriteString("interface wlan0\nnohook wpa_supplicant\n")
		if metric != "" {
			out.WriteString(metric + "\n")
		}
	}
	return out.Bytes()
}

func writeLines(out *bytes.Buffer, lines []string) {
	for _, line := range lines {
		out.WriteString(line + "\n")
	}
}

// Write renders confPath for the mode in modePath and replaces outPath.
// A missing or unreadable confPath renders as nil.
func Write(confPath, modePath, outPath string) error {
	conf, _ := os.ReadFile(confPath)
	return atomicfile.Write(outPath, Render(conf, ReadMode(modePath)), atomicfile.Options{Mode: 0644, UID: -1, GID: -1})
}

// DeniesWlan0 reports whether a rendered config keeps dhcpcd off wlan0, as
// the AP render does.
func DeniesWlan0(rendered []byte) bool {
	for _, line := range strings.Split(string(rendered), "\n") {
		if strings.Join(strings.Fields(line), " ") == "denyinterfaces wlan0" {
			return true
		}
	}
	return false
}
