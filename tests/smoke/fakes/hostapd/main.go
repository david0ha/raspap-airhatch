// Command hostapd stands in for hostapd in the Docker smoke test, where wlan0
// is one end of a veth pair. The real hostapd cannot reach state=ENABLED
// there: RaspAP's country_code keeps it in COUNTRY_UPDATE without a wireless
// regulatory domain, and its wired driver binds UDP port 67, which dnsmasq
// already holds when airhatchd starts hostapd.
//
// It reads RaspAP's hostapd.conf as hostapd does (interface, ctrl_interface,
// ssid) and answers the control-socket commands airhatchd sends. The
// station count is read from the file named by -stations (absent means 0),
// so a test can model a phone on the hotspot.
//
// Usage: hostapd [-stations <file>] <hostapd.conf>
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type apConfig struct {
	iface   string
	ctrlDir string
	ssid    string
}

func main() {
	stations := flag.String("stations", "", "file holding the station count")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: hostapd [-stations <file>] <hostapd.conf>")
	}
	cfg, err := readConfig(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", cfg.iface)); err != nil {
		log.Fatalf("interface %s: %v", cfg.iface, err)
	}
	conn, path, err := listen(cfg)
	if err != nil {
		log.Fatal(err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		conn.Close()
	}()
	log.Printf("%s: AP-ENABLED ssid=%q (fake)", cfg.iface, cfg.ssid)
	serve(conn, cfg, *stations)
	if err := os.Remove(path); err != nil {
		log.Fatal(err)
	}
}

// readConfig keeps the last value of each key, as hostapd does.
func readConfig(path string) (apConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return apConfig{}, err
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && !strings.HasPrefix(key, "#") {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return apConfig{}, err
	}
	cfg := apConfig{iface: values["interface"], ctrlDir: values["ctrl_interface"], ssid: values["ssid"]}
	if cfg.iface == "" || cfg.ctrlDir == "" || cfg.ssid == "" {
		return apConfig{}, fmt.Errorf("%s: interface, ctrl_interface and ssid are required", path)
	}
	if strings.HasPrefix(cfg.ctrlDir, "DIR=") {
		return apConfig{}, fmt.Errorf("%s: ctrl_interface=DIR=… is not supported", path)
	}
	return cfg, nil
}

func listen(cfg apConfig) (*net.UnixConn, string, error) {
	if err := os.MkdirAll(cfg.ctrlDir, 0o770); err != nil {
		return nil, "", err
	}
	path := filepath.Join(cfg.ctrlDir, cfg.iface)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, "", err
	}
	return conn, path, os.Chmod(path, 0o770)
}

func serve(conn *net.UnixConn, cfg apConfig, stationsFile string) {
	buf := make([]byte, 4096)
	for {
		n, peer, err := conn.ReadFromUnix(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			log.Printf("read: %v", err)
			continue
		}
		if peer == nil {
			continue
		}
		reply := answer(strings.TrimSpace(string(buf[:n])), cfg, stationsFile)
		if _, err := conn.WriteToUnix([]byte(reply), peer); err != nil {
			log.Printf("reply to %s: %v", peer.Name, err)
		}
	}
}

func answer(command string, cfg apConfig, stationsFile string) string {
	switch command {
	case "PING":
		return "PONG\n"
	case "ATTACH", "DETACH":
		return "OK\n"
	case "STATUS":
		return fmt.Sprintf("state=ENABLED\nphy=fake0\nfreq=2412\nnum_sta[0]=%d\nssid[0]=%s\nbss[0]=%s\n",
			stationCount(stationsFile), cfg.ssid, cfg.iface)
	default:
		return "UNKNOWN COMMAND\n"
	}
}

func stationCount(file string) int {
	if file == "" {
		return 0
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		log.Printf("stations: %v", err)
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		log.Printf("stations: %v", err)
		return 0
	}
	return count
}
