package repository

import (
	"testing"

	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
)

func TestValidateAccountListSortRejectsPostgresOnlyBillingRateSort(t *testing.T) {
	for _, dbDialect := range []string{dialect.MySQL, dialect.SQLite} {
		err := validateAccountListSort("upstream_billing_rate", dbDialect)
		require.EqualError(t, err, "sorting by upstream_billing_rate is only supported on PostgreSQL")
	}
	require.NoError(t, validateAccountListSort("upstream_billing_rate", dialect.Postgres))
	require.NoError(t, validateAccountListSort("priority", dialect.MySQL))
}
