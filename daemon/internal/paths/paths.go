// Package paths names every file, socket and unit airhatchd touches
// (docs/API.md §1-2).
package paths

import "path/filepath"

// Iface is the only interface airhatchd manages.
const Iface = "wlan0"

// PortalPort is the captive portal listener's port on the AP address.
const PortalPort = 8099

// WebUID and WebGID are www-data in the meta-raspap image.
const (
	WebUID = 33
	WebGID = 33
)

// Units airhatchd starts, stops or inspects.
const (
	SupplicantUnit = "airhatch-wpa-supplicant.service"
	HostapdUnit    = "hostapd.service"
	// HostapdInstances matches RaspAP's hostapd@.service instances.
	HostapdInstances    = "hostapd@*.service"
	DnsmasqUnit         = "dnsmasq.service"
	DhcpcdUnit          = "dhcpcd.service"
	NetworkActivityUnit = "raspap-network-activity@wlan0.service"
)

// Layout holds every filesystem path. Production code uses Default; tests
// relocate the whole tree under a temporary directory with Rooted.
type Layout struct {
	RunDir        string
	APLease       string
	ModeFile      string
	DnsmasqConf   string
	DhcpcdConf    string
	APISock       string
	FirstbootLock string

	DaemonConf    string
	FirstbootDone string
	Notice        string

	CtrlDir     string
	CtrlStandIn string
	HostapdCtrl string
	ScanCache   string

	HostapdConf  string
	HostapdIni   string
	RaspAPDhcpcd string
	WpaConf      string
	LighttpdConf string
	RaspAPAuth   string
	WlanMAC      string
	MachineID    string
	Console      string
	Proc         string
}

// Default is the layout of the image.
func Default() Layout {
	return Layout{
		RunDir:        "/run/airhatch",
		APLease:       "/run/airhatch/ap.lease",
		ModeFile:      "/run/airhatch/mode",
		DnsmasqConf:   "/run/airhatch/dnsmasq.conf",
		DhcpcdConf:    "/run/airhatch/dhcpcd.conf",
		APISock:       "/run/airhatch/api.sock",
		FirstbootLock: "/run/airhatch/firstboot.lock",

		DaemonConf:    "/etc/airhatch/airhatchd.conf",
		FirstbootDone: "/etc/airhatch/firstboot.done",
		Notice:        "/boot/raspap-airhatch.txt",

		CtrlDir:     "/var/run/wpa_supplicant",
		CtrlStandIn: "/var/run/wpa_supplicant/wlan0",
		HostapdCtrl: "/var/run/hostapd/wlan0",
		ScanCache:   "/tmp/raspap",

		HostapdConf:  "/etc/hostapd/hostapd.conf",
		HostapdIni:   "/etc/raspap/hostapd.ini",
		RaspAPDhcpcd: "/etc/dhcpcd.conf",
		WpaConf:      "/etc/wpa_supplicant/wpa_supplicant.conf",
		LighttpdConf: "/etc/lighttpd/lighttpd.conf",
		RaspAPAuth:   "/etc/raspap/raspap.auth",
		WlanMAC:      "/sys/class/net/wlan0/address",
		MachineID:    "/etc/machine-id",
		Console:      "/dev/console",
		Proc:         "/proc",
	}
}

// Rooted returns the default layout with every path moved under root.
func Rooted(root string) Layout {
	l := Default()
	for _, p := range l.fields() {
		*p = filepath.Join(root, *p)
	}
	return l
}

func (l *Layout) fields() []*string {
	return []*string{
		&l.RunDir, &l.APLease, &l.ModeFile, &l.DnsmasqConf, &l.DhcpcdConf, &l.APISock, &l.FirstbootLock,
		&l.DaemonConf, &l.FirstbootDone, &l.Notice,
		&l.CtrlDir, &l.CtrlStandIn, &l.HostapdCtrl, &l.ScanCache,
		&l.HostapdConf, &l.HostapdIni, &l.RaspAPDhcpcd, &l.WpaConf, &l.LighttpdConf, &l.RaspAPAuth,
		&l.WlanMAC, &l.MachineID, &l.Console, &l.Proc,
	}
}
