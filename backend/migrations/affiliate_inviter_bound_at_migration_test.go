package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAffiliateInviterBoundAtMigration_PreservesExistingBindings(t *testing.T) {
	content, err := FS.ReadFile("250_affiliate_inviter_bound_at.sql")
	require.NoError(t, err)
	query := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, query, "ADD COLUMN IF NOT EXISTS inviter_bound_at TIMESTAMPTZ")
	require.Contains(t, query, "SET inviter_bound_at = created_at WHERE inviter_id IS NOT NULL AND inviter_bound_at IS NULL")
	require.NotContains(t, query, "SET inviter_bound_at = NOW()")
}
