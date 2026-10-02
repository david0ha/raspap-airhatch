package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
)

// pluginFixtures are the status objects the plugin's harness renders.
const pluginFixtures = "../../../plugin/tests/fixtures/"

// keyPaths lists the dotted key paths of a decoded JSON object. objects
// holds the paths whose value is a non-null object.
func keyPaths(v any, prefix string, paths, objects map[string]bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	objects[prefix] = true
	for k, child := range m {
		path := strings.TrimPrefix(prefix+"."+k, ".")
		paths[path] = true
		keyPaths(child, path, paths, objects)
	}
}

func decodedKeys(t *testing.T, data []byte) (paths, objects map[string]bool) {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	paths, objects = map[string]bool{}, map[string]bool{}
	keyPaths(v, "", paths, objects)
	return paths, objects
}

// TestPluginFixturesMatchStatusJSON keeps the plugin's hand-written fixtures
// in step with the daemon's status object: a renamed, added or removed key
// fails here instead of on the device's plugin page.
func TestPluginFixturesMatchStatusJSON(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	creds := "/boot/raspap-airhatch.txt"
	full := Status{
		Operation: &Operation{Name: policy.ClientAttempt, StartedAt: now},
		HoldUntil: &now,
		Client:    &ClientStatus{SSID: "Home", IPv4: "192.168.1.23/24"},
		AP:        &APStatus{SSID: "RaspAP", IPv4: "10.3.141.1/24"},
		Timers: Timers{AttemptEndsAt: &now, GraceEndsAt: &now, RetryAt: &now, BusyRetryAt: &now,
			HandoffAt: &now, NextOpenAt: &now},
		LastError:       &LastError{Code: "connect_timeout", Message: "m", At: now},
		Notices:         []string{},
		CredentialsFile: &creds,
	}
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := decodedKeys(t, data)

	fixtures, err := filepath.Glob(pluginFixtures + "*.json")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no plugin fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		got, objects := decodedKeys(t, data)
		for path := range got {
			if !want[path] {
				t.Errorf("%s: key %q is not in the daemon's status", filepath.Base(fixture), path)
			}
		}
		for path := range want {
			parent := ""
			if i := strings.LastIndex(path, "."); i >= 0 {
				parent = path[:i]
			}
			if objects[parent] && !got[path] {
				t.Errorf("%s: misses the daemon's key %q", filepath.Base(fixture), path)
			}
		}
	}
}
