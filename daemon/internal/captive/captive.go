// Package captive holds the captive portal of the fallback hotspot: the nft
// table, the dnsmasq include and the HTTP listener (docs/DESIGN.md §6).
package captive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// OpenBatch replaces the airhatch table atomically: adding an existing
// table is a no-op, so the add+delete pair works whether it existed or not.
const OpenBatch = `add table inet airhatch
delete table inet airhatch
table inet airhatch {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "wlan0" udp dport 53 redirect to :53
    iifname "wlan0" tcp dport 53 redirect to :53
    iifname "wlan0" tcp dport 80 redirect to :8099
  }
  chain input {
    type filter hook input priority filter; policy accept;
    iifname "wlan0" tcp dport { 443, 853 } reject with tcp reset
  }
  chain forward {
    type filter hook forward priority filter; policy accept;
    iifname "wlan0" drop
    oifname "wlan0" drop
  }
}
`

// CloseBatch removes the airhatch table, present or not.
const CloseBatch = "add table inet airhatch\ndelete table inet airhatch\n"

// DnsmasqClient keeps dnsmasq off wlan0 (the content tmpfiles writes at boot).
const DnsmasqClient = "# airhatch: client\nexcept-interface=wlan0\n"

// IsClientInclude reports whether a dnsmasq include is the client fence:
// DnsmasqClient, or the line tmpfiles writes at boot.
func IsClientInclude(include []byte) bool {
	return string(include) == DnsmasqClient || strings.TrimSpace(string(include)) == "except-interface=wlan0"
}

// DnsmasqAP is the AP-mode include: a DNS wildcard to the AP address (kept
// off the device's own lookups by except-interface=lo) and DHCP option 114.
func DnsmasqAP(apIP netip.Addr, captive bool) string {
	if !captive {
		return "# airhatch: ap\n"
	}
	return fmt.Sprintf("# airhatch: ap\naddress=/#/%s\nexcept-interface=lo\ndhcp-option=tag:wlan0,114,\"http://%s/captive-portal/api\"\n", apIP, apIP)
}

// Landing is the absolute URL phones are sent to: RaspAP's login, then its
// WiFi client page, or the plugin page after a failed hand-off.
func Landing(apIP netip.Addr, afterFailedHandoff bool) string {
	page := "/wpa_conf"
	if afterFailedHandoff {
		page = "/plugin__Airhatch"
	}
	return "http://" + apIP.String() + "/login?action=" + url.QueryEscape(page)
}

// Handler serves the RFC 8908 API on any Host, proxies requests for the AP
// address to lighttpd, and redirects every other Host to landing().
func Handler(apIP netip.Addr, lighttpdPort int, landing func() string) http.Handler {
	target := &url.URL{Scheme: "http", Host: netip.AddrPortFrom(apIP, uint16(lighttpdPort)).String()}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.SetXForwarded()
		r.Out.Host = r.In.Host
	}}
	own := map[string]bool{apIP.String(): true, apIP.String() + ":80": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/captive-portal/api":
			w.Header().Set("Content-Type", "application/captive+json")
			w.Header().Set("Cache-Control", "no-store")
			body, _ := json.Marshal(struct {
				Captive bool   `json:"captive"`
				Portal  string `json:"user-portal-url"`
			}{true, landing()})
			_, _ = w.Write(body)
		case own[r.Host]:
			proxy.ServeHTTP(w, r)
		default:
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Location", landing())
			w.WriteHeader(http.StatusFound)
		}
	})
}

// Portal is the running listener.
type Portal struct {
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

// Listen serves handler on addr (the AP address, never a wildcard).
func Listen(addr netip.AddrPort, handler http.Handler) (*Portal, error) {
	listener, err := net.Listen("tcp", addr.String())
	if err != nil {
		return nil, err
	}
	p := &Portal{
		server:   &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second},
		listener: listener,
		done:     make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		_ = p.server.Serve(listener)
	}()
	return p, nil
}

// Addr is the bound address.
func (p *Portal) Addr() netip.AddrPort {
	return p.listener.Addr().(*net.TCPAddr).AddrPort()
}

// Close stops the listener and waits for in-flight requests up to 2 s.
func (p *Portal) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := p.server.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = p.server.Close()
	}
	<-p.done
	return err
}
