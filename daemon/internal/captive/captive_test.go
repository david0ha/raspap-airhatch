package captive

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var apIP = netip.MustParseAddr("10.3.141.1")

func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/captive/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGoldenTexts(t *testing.T) {
	cases := map[string]string{
		"nft-open.golden":             OpenBatch,
		"nft-close.golden":            CloseBatch,
		"dnsmasq-client.golden":       DnsmasqClient,
		"dnsmasq-ap.golden":           DnsmasqAP(apIP, true),
		"dnsmasq-ap-nocaptive.golden": DnsmasqAP(apIP, false),
	}
	for name, got := range cases {
		if want := golden(t, name); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
	}
}

func TestLanding(t *testing.T) {
	if got := Landing(apIP, false); got != "http://10.3.141.1/login?action=%2Fwpa_conf" {
		t.Fatal(got)
	}
	if got := Landing(apIP, true); got != "http://10.3.141.1/login?action=%2Fplugin__Airhatch" {
		t.Fatal(got)
	}
}

// upstream is a fake lighttpd that echoes what it received.
func upstream(t *testing.T) int {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Host", r.Host)
		w.Header().Set("X-Seen-XFF", r.Header.Get("X-Forwarded-For"))
		io.WriteString(w, "raspap "+r.URL.RequestURI())
	}))
	t.Cleanup(server.Close)
	port, _ := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
	return port
}

func TestPortalRedirectsForeignHosts(t *testing.T) {
	landing := Landing(apIP, false)
	h := Handler(apIP, 80, func() string { return landing })
	for _, host := range []string{"connectivitycheck.gstatic.com", "captive.apple.com", "10.3.141.1:8099", "example.com:80"} {
		req := httptest.NewRequest("GET", "http://"+host+"/generate_204", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != landing || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %v", host, rec.Code, rec.Header())
		}
	}
}

func TestPortalLandingFollowsFailedHandoff(t *testing.T) {
	failed := false
	h := Handler(apIP, 80, func() string { return Landing(apIP, failed) })
	get := func() string {
		req := httptest.NewRequest("GET", "http://probe.example/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}
	if get() != Landing(apIP, false) {
		t.Fatal("initial landing")
	}
	failed = true
	if get() != "http://10.3.141.1/login?action=%2Fplugin__Airhatch" {
		t.Fatal("landing did not switch after a failed hand-off")
	}
}

func TestCaptiveAPIOnAnyHost(t *testing.T) {
	h := Handler(apIP, 80, func() string { return Landing(apIP, false) })
	for _, host := range []string{"10.3.141.1", "anything.example"} {
		req := httptest.NewRequest("GET", "http://"+host+"/captive-portal/api", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/captive+json" ||
			rec.Header().Get("Cache-Control") != "no-store" ||
			body != `{"captive":true,"user-portal-url":"http://10.3.141.1/login?action=%2Fwpa_conf"}` {
			t.Errorf("%s: %d %v %s", host, rec.Code, rec.Header(), body)
		}
	}
}

func TestAPHostIsProxiedToLighttpd(t *testing.T) {
	port := upstream(t)
	loopback := netip.MustParseAddr("127.0.0.1")
	h := Handler(loopback, port, func() string { return Landing(loopback, false) })
	for _, host := range []string{"127.0.0.1", "127.0.0.1:80"} {
		req := httptest.NewRequest("GET", "http://"+host+"/wpa_conf?x=1", nil)
		req.Host = host
		req.RemoteAddr = "10.3.141.77:50000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.String() != "raspap /wpa_conf?x=1" ||
			rec.Header().Get("X-Seen-XFF") != "10.3.141.77" || rec.Header().Get("X-Seen-Host") != host {
			t.Errorf("%s: %d %q %v", host, rec.Code, rec.Body.String(), rec.Header())
		}
	}
}

func TestPortalListensOnAPAddressOnly(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	port := freePort(t)
	p, err := Listen(netip.AddrPortFrom(loopback, uint16(port)), Handler(loopback, 80, func() string { return "http://x/" }))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Addr(); got != netip.AddrPortFrom(loopback, uint16(port)) {
		t.Fatalf("bound %v", got)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/hotspot-detect.html")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond); err == nil {
		t.Fatal("listener still open after Close")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	u, _ := url.Parse("tcp://" + l.Addr().String())
	port, _ := strconv.Atoi(u.Port())
	return port
}

// The include tmpfiles creates at boot (system/tmpfiles.d) is a client fence
// as well as the content close_ap writes; the AP contents are not.
func TestIsClientInclude(t *testing.T) {
	tmpfiles, err := os.ReadFile("../../../system/tmpfiles.d/raspap-airhatch.conf")
	if err != nil {
		t.Fatal(err)
	}
	var boot string
	for _, line := range strings.Split(string(tmpfiles), "\n") {
		if fields := strings.Fields(line); len(fields) == 7 && fields[0] == "f" && fields[1] == "/run/airhatch/dnsmasq.conf" {
			boot = fields[6]
		}
	}
	ap := netip.MustParseAddr("10.3.141.1")
	for include, want := range map[string]bool{
		boot: true, boot + "\n": true, DnsmasqClient: true,
		DnsmasqAP(ap, true): false, DnsmasqAP(ap, false): false, "": false,
	} {
		if IsClientInclude([]byte(include)) != want {
			t.Errorf("%q: want %v", include, want)
		}
	}
	if boot == "" {
		t.Fatal("no dnsmasq.conf line in the tmpfiles file")
	}
}
