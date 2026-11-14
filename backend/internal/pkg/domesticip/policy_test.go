package domesticip

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSnapshot(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(Snapshot{Version: 1, GeneratedAt: "2026-11-13T00:00:00Z", CNCIDRs: []string{"1.0.1.0/24", "240e::/20"}, ManualIPs: []string{"1.0.1.8"}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testOptions(t *testing.T) Options {
	t.Helper()
	data := testSnapshot(t)
	digest := sha256.Sum256(data)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])}
}

func TestNormalizePublicIP(t *testing.T) {
	valid := map[string]string{"8.8.8.8": "8.8.8.8", " ::ffff:8.8.8.8 ": "8.8.8.8", "240E:0000::12": "240e::12", "2606:4700:4700::1111": "2606:4700:4700::1111"}
	for input, want := range valid {
		got, err := NormalizePublicIP(input)
		if err != nil || got != want {
			t.Errorf("%q => %q, %v; want %q", input, got, err, want)
		}
	}
	invalid := []string{"", "localhost", "8.8.8.8:80", "8.8.8.8/32", "8.8.8.8, 1.1.1.1", "[2606:4700::1]", "240e::1%eth0", "0.0.0.0", "10.1.2.3", "100.64.0.1", "100.127.255.254", "127.0.0.1", "169.254.169.254", "172.16.0.1", "172.31.255.255", "192.168.1.1", "192.0.0.1", "192.0.2.1", "192.88.99.1", "198.18.1.1", "198.51.100.2", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::", "::1", "::ffff:10.1.2.3", "fc00::1", "fe80::1", "ff02::1", "64:ff9b::808:808", "100::1", "2001:db8::1", "2001:20::1", "2002:808:808::1", "3fff::1", "4000::1"}
	for _, input := range invalid {
		if got, err := NormalizePublicIP(input); err == nil {
			t.Errorf("accepted non-public %q as %q", input, got)
		}
	}
}

func TestPolicyClientTrustAndDecision(t *testing.T) {
	p, err := NewPolicy(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer, forwarded string
		required, reject      bool
	}{
		{"domestic", "127.0.0.1:1234", "1.0.1.7", true, false},
		{"manual", "127.0.0.1:1234", "1.0.1.8", false, false},
		{"foreign", "127.0.0.1:1234", "8.8.8.8", false, false},
		{"domestic ipv6", "[::1]:1234", "240e::7", true, false},
		{"mapped", "127.0.0.1:1234", "::ffff:1.0.1.7", true, false},
		{"direct spoof", "8.8.8.8:1234", "1.0.1.8", false, true},
		{"no header", "127.0.0.1:1234", "", false, true},
		{"chain", "127.0.0.1:1234", "1.0.1.8, 8.8.8.8", false, true},
		{"internal", "127.0.0.1:1234", "192.168.1.1", false, true},
		{"bare peer", "127.0.0.1", "8.8.8.8", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://example.test/v1/models", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("CF-Connecting-IP", "1.0.1.8")
			r.Header.Set("X-Real-IP", "1.0.1.8")
			_, required, err := p.NeedsGrant(r)
			if (err != nil) != tc.reject || required != tc.required {
				t.Fatalf("required=%v err=%v", required, err)
			}
		})
	}
	r := httptest.NewRequest("GET", "https://example.test/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "1.0.1.8")
	r.Header.Add("X-Forwarded-For", "8.8.8.8")
	if _, err := p.ResolveClientIP(r); err == nil {
		t.Fatal("accepted duplicate XFF headers")
	}
}

func TestPolicyDisabledAndInvalidConfiguration(t *testing.T) {
	p, err := NewPolicy(Options{})
	if err != nil || p.Enabled() {
		t.Fatalf("disabled policy: %v", err)
	}
	if _, required, err := p.NeedsGrant(nil); err != nil || required {
		t.Fatal("disabled policy rejected existing traffic")
	}
	for _, mutation := range []func(*Options){
		func(o *Options) { o.SnapshotFile = "missing" },
		func(o *Options) { o.SnapshotSHA256 = "" },
		func(o *Options) { o.SnapshotSHA256 = strings.Repeat("0", 64) },
		func(o *Options) { o.TrustedProxyIPs = []string{"0.0.0.0/0"} },
		func(o *Options) { o.TrustedProxyIPs = []string{"10.0.0.1"} },
		func(o *Options) { o.TrustedProxyIPs = []string{"127.0.0.1/32"} },
	} {
		o := testOptions(t)
		mutation(&o)
		if _, err := NewPolicy(o); err == nil {
			t.Fatal("accepted unsafe enabled configuration")
		}
	}
}

func TestValidateSnapshotRejectsIncompleteAndReservedData(t *testing.T) {
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Version = 2 },
		func(s *Snapshot) { s.GeneratedAt = "" },
		func(s *Snapshot) { s.CNCIDRs = nil },
		func(s *Snapshot) { s.CNCIDRs = []string{"1.0.1.0/24"} },
		func(s *Snapshot) { s.CNCIDRs = append(s.CNCIDRs, "1.0.1.7/24") },
		func(s *Snapshot) { s.CNCIDRs = append(s.CNCIDRs, "0.0.0.0/0") },
		func(s *Snapshot) { s.CNCIDRs = append(s.CNCIDRs, "192.0.0.0/8") },
		func(s *Snapshot) { s.CNCIDRs = append(s.CNCIDRs, "2000::/3") },
		func(s *Snapshot) { s.CNCIDRs = append(s.CNCIDRs, "1.0.1.0/24") },
		func(s *Snapshot) { s.ManualIPs = []string{"192.168.1.1"} },
		func(s *Snapshot) { s.ManualIPs = []string{"8.8.8.8/32"} },
		func(s *Snapshot) { s.ManualIPs = []string{"8.8.8.8", "8.8.8.8"} },
	} {
		var s Snapshot
		if err := json.Unmarshal(testSnapshot(t), &s); err != nil {
			t.Fatal(err)
		}
		mutate(&s)
		data, _ := json.Marshal(s)
		if _, err := ValidateSnapshot(data); err == nil {
			t.Fatalf("accepted invalid snapshot %s", data)
		}
	}
	for _, data := range [][]byte{append(testSnapshot(t), []byte("{}")...), []byte(`{"unexpected":true}`)} {
		if _, err := ValidateSnapshot(data); err == nil {
			t.Fatal("accepted ambiguous JSON")
		}
	}
}
