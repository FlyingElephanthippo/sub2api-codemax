package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	"github.com/stretchr/testify/require"
)

type domesticIPLookupStub struct {
	allowed bool
	err     error
	calls   int
}

func (s *domesticIPLookupStub) HasActiveGrant(context.Context, string) (bool, error) {
	s.calls++
	return s.allowed, s.err
}

func domesticIPAccessPolicy(t *testing.T) *domesticip.Policy {
	t.Helper()
	data := []byte(`{"version":1,"generated_at":"2026-11-13T00:00:00Z","cn_cidrs":["1.0.1.0/24","240e::/20"],"manual_ips":["1.0.1.8"]}`)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	p, err := domesticip.NewPolicy(domesticip.Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])})
	require.NoError(t, err)
	return p
}

func TestDomesticIPAccessDecision(t *testing.T) {
	policy := domesticIPAccessPolicy(t)
	for _, tc := range []struct {
		name, ip string
		allow    bool
		dbErr    error
		want     error
		calls    int
	}{
		{"foreign unchanged", "8.8.8.8", false, errors.New("offline"), nil, 0},
		{"manual unchanged", "1.0.1.8", false, errors.New("offline"), nil, 0},
		{"domestic denied", "1.0.1.7", false, nil, ErrDomesticIPDenied, 1},
		{"domestic shared grant", "1.0.1.7", true, nil, nil, 1},
		{"domestic IPv6", "240e::7", true, nil, nil, 1},
		{"lookup failure closed", "1.0.1.7", false, errors.New("offline"), ErrDomesticIPUnavailable, 1},
		{"invalid source", "10.0.0.1", true, nil, ErrDomesticIPInvalid, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &domesticIPLookupStub{allowed: tc.allow, err: tc.dbErr}
			s := &DomesticIPAccessService{policy: policy, grants: lookup}
			r := httptest.NewRequest("POST", "/v1/responses", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("X-Forwarded-For", tc.ip)
			r.Header.Set("CF-Connecting-IP", "1.0.1.8")
			r.Header.Set("X-Real-IP", "1.0.1.8")
			err := s.Check(r)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			require.Equal(t, tc.calls, lookup.calls)
		})
	}
}

func TestDomesticIPAccessWiringFailsClosed(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/images/tasks/task", nil)
	s := &APIKeyService{cfg: &config.Config{DomesticIP: config.DomesticIPConfig{Enabled: true}}}
	require.ErrorIs(t, s.ValidateDomesticIP(r), ErrDomesticIPUnavailable)
	disabled, err := domesticip.NewPolicy(domesticip.Options{})
	require.NoError(t, err)
	s.domesticIPAccess = NewDomesticIPAccessService(disabled, nil)
	require.ErrorIs(t, s.ValidateDomesticIP(r), ErrDomesticIPUnavailable)
	s.cfg.DomesticIP.Enabled = false
	require.NoError(t, s.ValidateDomesticIP(r))
}
