package database_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/coderd/database/dbtestutil"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/testutil"
)

// TestUserSecretsLimitsSchemaConstants pins the per-user limit
// literals declared inside enforce_user_secrets_per_user_limits to
// the corresponding constants in codersdk so the two layers cannot
// silently drift apart.
func TestUserSecretsLimitsSchemaConstants(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.SkipNow()
	}

	ctx := testutil.Context(t, testutil.WaitMedium)
	_, _, sqlDB := dbtestutil.NewDBWithSQLDB(t)

	var triggerDef string
	err := sqlDB.QueryRowContext(ctx,
		`SELECT pg_get_functiondef('enforce_user_secrets_per_user_limits'::regproc)`,
	).Scan(&triggerDef)
	require.NoError(t, err)

	require.Contains(t, triggerDef, fmt.Sprintf(
		"count_limit       constant int    := %d;",
		codersdk.MaxUserSecretsPerUser,
	))
	require.Contains(t, triggerDef, fmt.Sprintf(
		"total_bytes_limit constant bigint := %d;",
		codersdk.MaxUserSecretsTotalValueBytes,
	))
	require.Contains(t, triggerDef, fmt.Sprintf(
		"env_bytes_limit   constant bigint := %d;",
		codersdk.MaxUserSecretsEnvValueBytes,
	))
}
