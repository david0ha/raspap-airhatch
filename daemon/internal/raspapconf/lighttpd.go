package raspapconf

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LighttpdPort is the top-level server.port of lighttpd.conf, 80 when unset.
func LighttpdPort(path string) (int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 80, nil
	}
	if err != nil {
		return 0, err
	}
	for _, line := range lines(data) {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "server.port" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("%s: bad server.port %q", path, strings.TrimSpace(value))
		}
		return port, nil
	}
	return 80, nil
}
