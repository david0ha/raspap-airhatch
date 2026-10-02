package radio

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"

	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

// Plan is what an AP open needs from RaspAP's files, read fresh each time.
type Plan struct {
	Prefix       netip.Prefix
	SSID         string
	LighttpdPort int
}

// placeholderPassphrases are AP passphrases anyone can guess: RaspAP's
// default and common placeholders, compared case-insensitively. An empty
// one is an open hotspot.
var placeholderPassphrases = []string{"", "changeme", "secret", "password", "12345678"}

// defaultLoginScript prints "default" when the hash on stdin's second line
// verifies RaspAP's default password on its first line, with the
// password_verify RaspAP's login uses (U:src/RaspAP/Auth/HTTPAuth.php:172-178).
const defaultLoginScript = `$password = rtrim(fgets(STDIN)); $hash = rtrim(fgets(STDIN)); echo password_verify($password, $hash) ? "default" : "custom";`

// Preflight checks the preconditions of docs/DESIGN.md §5.1. The admin login
// check runs php-cli, so it comes last and a refusal on a file runs nothing.
func (r *Radio) Preflight(ctx context.Context) (Plan, error) {
	ini, err := raspapconf.ReadHostapdIni(r.l.HostapdIni)
	if err != nil {
		return Plan{}, publicerror.Wrap(publicerror.UnsupportedRaspAPMode, "", err)
	}
	if ini.BridgedEnable || ini.WifiAPEnable {
		return Plan{}, publicerror.New(publicerror.UnsupportedRaspAPMode, "")
	}
	hostapd, err := raspapconf.ReadHostapd(r.l.HostapdConf)
	if err != nil || hostapd.Interface != "wlan0" {
		return Plan{}, publicerror.Wrap(publicerror.UnsupportedRaspAPMode, "", err)
	}
	if slices.Contains(placeholderPassphrases, strings.ToLower(hostapd.Passphrase)) {
		return Plan{}, publicerror.New(publicerror.DefaultAPPassphrase, "")
	}
	hash, err := raspapconf.ReadAuth(r.l.RaspAPAuth, r.webUID, r.webGID)
	if err != nil {
		return Plan{}, publicerror.Wrap(publicerror.DefaultAdminLogin, "", err)
	}
	dhcpcd, err := os.ReadFile(r.l.RaspAPDhcpcd)
	if err != nil {
		return Plan{}, publicerror.Wrap(publicerror.NoAPAddress, "", err)
	}
	prefix, ok := raspapconf.APAddress(dhcpcd)
	if !ok {
		return Plan{}, publicerror.New(publicerror.NoAPAddress, "")
	}
	port, err := raspapconf.LighttpdPort(r.l.LighttpdConf)
	if err != nil {
		return Plan{}, publicerror.Wrap(publicerror.APStartFailed, "", err)
	}
	if isDefault, err := isDefaultLogin(ctx, r.run, hash); err != nil || isDefault {
		return Plan{}, publicerror.Wrap(publicerror.DefaultAdminLogin, "", err)
	}
	return Plan{Prefix: prefix, SSID: hostapd.SSID, LighttpdPort: port}, nil
}

// isDefaultLogin reports whether hash verifies RaspAP's default password
// "secret". The password and the hash go to php-cli on stdin.
func isDefaultLogin(ctx context.Context, run runner.InputRunner, hash string) (bool, error) {
	out, err := run.RunInput(ctx, []byte("secret\n"+hash+"\n"), "php", "-r", defaultLoginScript)
	if err != nil {
		return false, fmt.Errorf("check the admin login: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "default":
		return true, nil
	case "custom":
		return false, nil
	}
	return false, fmt.Errorf("check the admin login: php printed %q", out)
}
