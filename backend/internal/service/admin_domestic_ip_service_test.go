package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	"github.com/stretchr/testify/require"
)

type adminDomesticIPRepoStub struct {
	AdminDomesticIPRepository
	addedIP, addedNote string
	actor              AdminDomesticIPActor
	deleted            int64
	search             string
	listCalls          int
}

func (r *adminDomesticIPRepoStub) Add(_ context.Context, ip, note string, actor AdminDomesticIPActor) (*AdminDomesticIPEntry, error) {
	r.addedIP, r.addedNote, r.actor = ip, note, actor
	return &AdminDomesticIPEntry{ID: 1, IP: ip, Note: note}, nil
}

func (r *adminDomesticIPRepoStub) Delete(_ context.Context, id int64, actor AdminDomesticIPActor) error {
	r.deleted, r.actor = id, actor
	return nil
}

func (r *adminDomesticIPRepoStub) List(_ context.Context, _, _ int, search string) ([]AdminDomesticIPEntry, int64, error) {
	r.search = search
	r.listCalls++
	return nil, 0, nil
}

func TestAdminDomesticIPSearchUnicodeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, search string
		valid        bool
	}{
		{"empty", "", true},
		{"trimmed", "  办公室  ", true},
		{"previous byte boundary", strings.Repeat("字", 43), true},
		{"200 ASCII", strings.Repeat("a", 200), true},
		{"200 Chinese", strings.Repeat("字", 200), true},
		{"200 supplementary Unicode", strings.Repeat("😀", 200), true},
		{"201 ASCII", strings.Repeat("a", 201), false},
		{"201 Chinese", strings.Repeat("字", 201), false},
		{"invalid UTF8", string([]byte{0xff}), false},
		{"embedded NUL", "office\x00IP", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &adminDomesticIPRepoStub{}
			svc := NewAdminDomesticIPService(repo, domesticIPAccessPolicy(t))
			_, err := svc.List(context.Background(), 1, 20, tc.search)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, 1, repo.listCalls)
				require.Equal(t, strings.TrimSpace(tc.search), repo.search)
			} else {
				require.Error(t, err)
				require.Zero(t, repo.listCalls)
			}
		})
	}
}

func TestAdminDomesticIPValidatesPublicHostsAndRealActor(t *testing.T) {
	actor := AdminDomesticIPActor{UserID: 1, AuthMethod: "jwt", SourceIP: "127.0.0.1"}
	for _, raw := range []string{"", "10.0.0.1", "127.0.0.1", "192.0.2.1", "1.0.1.0/24", "240e::/20", "fe80::1%eth0", "::1", "1.0.1.1, 1.0.1.2"} {
		t.Run(raw, func(t *testing.T) {
			repo := &adminDomesticIPRepoStub{}
			svc := NewAdminDomesticIPService(repo, domesticIPAccessPolicy(t))
			_, err := svc.Add(context.Background(), raw, "", actor)
			require.Error(t, err)
			require.Empty(t, repo.addedIP)
		})
	}
	repo := &adminDomesticIPRepoStub{}
	svc := NewAdminDomesticIPService(repo, domesticIPAccessPolicy(t))
	for _, badActor := range []AdminDomesticIPActor{{}, {UserID: 1, AuthMethod: "user"}, {UserID: 1, AuthMethod: "jwt", SourceIP: "not-an-ip"}} {
		_, err := svc.Add(context.Background(), "1.0.1.7", "", badActor)
		require.Error(t, err)
		require.Error(t, svc.Delete(context.Background(), 1, badActor))
	}
	_, err := svc.Add(context.Background(), " 240e:0000::7 ", "  office  ", actor)
	require.NoError(t, err)
	require.Equal(t, "240e::7", repo.addedIP)
	require.Equal(t, "office", repo.addedNote)
	require.Equal(t, actor, repo.actor)
	_, err = svc.Add(context.Background(), "1.0.1.7", strings.Repeat("字", 501), actor)
	require.Error(t, err)
	require.Error(t, svc.Delete(context.Background(), -1, actor))
}

func TestAdminDomesticIPStaticInventoryIsDetachedAndFeatureDisabled(t *testing.T) {
	repo := &adminDomesticIPRepoStub{}
	policy := domesticIPAccessPolicy(t)
	svc := NewAdminDomesticIPService(repo, policy)
	result, err := svc.List(context.Background(), 1, 20, "")
	require.NoError(t, err)
	require.True(t, result.Enabled)
	require.Equal(t, []string{"1.0.1.8"}, result.StaticIPs)
	require.NotNil(t, result.Items)
	result.StaticIPs[0] = "1.0.1.9"
	require.True(t, policy.IsManualAllowed("1.0.1.8"))
	require.False(t, policy.IsManualAllowed("1.0.1.9"))
	disabled, err := domesticip.NewPolicy(domesticip.Options{})
	require.NoError(t, err)
	svc = NewAdminDomesticIPService(repo, disabled)
	actor := AdminDomesticIPActor{UserID: 1, AuthMethod: "admin_api_key"}
	_, err = svc.Add(context.Background(), "1.0.1.7", "", actor)
	require.Error(t, err)
	require.NoError(t, svc.Delete(context.Background(), 2, actor), "revocation remains possible while disabled")
	require.EqualValues(t, 2, repo.deleted)
}
