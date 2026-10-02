// Package publicerror is the closed vocabulary of errors shown to RaspAP
// users (docs/API.md §3). Messages never carry secrets.
package publicerror

import (
	"errors"
	"fmt"
)

// Code is a stable identifier the plugin may switch on.
type Code string

const (
	WrongPassword          Code = "wrong_password"
	NetworkNotFound        Code = "network_not_found"
	NoDHCPLease            Code = "no_dhcp_lease"
	ConnectTimeout         Code = "connect_timeout"
	NoSavedNetwork         Code = "no_saved_network"
	WpaConfUnusable        Code = "wpa_conf_unusable"
	UnsupportedRaspAPMode  Code = "unsupported_raspap_mode"
	DefaultAPPassphrase    Code = "default_ap_passphrase"
	DefaultAdminLogin      Code = "default_admin_login"
	NoAPAddress            Code = "no_ap_address"
	APStartFailed          Code = "ap_start_failed"
	APServiceOutsideDaemon Code = "ap_service_outside_daemon"
)

// Error is a public failure; it also wraps the internal cause for the journal.
type Error struct {
	Code    Code
	Message string
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.cause)
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.cause }

// New builds the public error for code. ssid names the network concerned,
// or is empty when no single network is.
func New(code Code, ssid string) *Error {
	return &Error{Code: code, Message: message(code, ssid)}
}

// Wrap is New with the internal cause kept for logs.
func Wrap(code Code, ssid string, cause error) *Error {
	e := New(code, ssid)
	e.cause = cause
	return e
}

// As returns the public error in err's chain, or nil.
func As(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	return nil
}

func message(code Code, ssid string) string {
	network := "the saved network"
	if ssid != "" {
		network = fmt.Sprintf("%q", ssid)
	}
	switch code {
	case WrongPassword:
		return fmt.Sprintf("The password for %s was rejected.", network)
	case NetworkNotFound:
		return fmt.Sprintf("Could not find %s nearby.", network)
	case NoDHCPLease:
		return fmt.Sprintf("Joined %s but got no IP address from it.", network)
	case ConnectTimeout:
		return fmt.Sprintf("Could not connect to %s in time.", network)
	case NoSavedNetwork:
		return "No Wi-Fi network is saved yet. Add one on RaspAP's WiFi client page."
	case WpaConfUnusable:
		return "RaspAP's wpa_supplicant.conf has no ctrl_interface for /var/run/wpa_supplicant."
	case UnsupportedRaspAPMode:
		return "RaspAP's bridged or AP+client mode is selected; raspap-airhatch supports only the hotspot on wlan0."
	case DefaultAPPassphrase:
		return "The hotspot passphrase is RaspAP's default, a common placeholder or empty, so the hotspot stays closed."
	case DefaultAdminLogin:
		return "RaspAP's admin login is still admin/secret, or raspap.auth is missing or unreadable by the web server, so the hotspot stays closed."
	case NoAPAddress:
		return "RaspAP's DHCP settings have no static address for wlan0, so the hotspot cannot open."
	case APStartFailed:
		return "The hotspot failed to start."
	case APServiceOutsideDaemon:
		return "hostapd was started on wlan0 outside airhatchd while Wi-Fi was a client; it was stopped."
	}
	return string(code)
}
