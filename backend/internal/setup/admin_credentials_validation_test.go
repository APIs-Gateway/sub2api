package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These validators existed on the old implementation as well; the unchanged
// fixture can prove the old login/bcrypt boundary contracts RED.
func TestSetupAdminCredentials_LoginAndBcryptValidation(t *testing.T) {
	for _, email := range []string{"a@b", "Name <a@b.com>", "not-email", strings.Repeat("x", 250) + "@a.com"} {
		t.Run("invalid_email/"+email, func(t *testing.T) { require.False(t, validateEmail(email)) })
	}
	for _, password := range []string{"1234567", strings.Repeat("x", 73), strings.Repeat("界", 25)} {
		t.Run("invalid_password/"+password, func(t *testing.T) { require.Error(t, validatePassword(password)) })
	}
	for _, password := range []string{"12345678", strings.Repeat("x", 72), strings.Repeat("界", 24)} {
		t.Run("valid_password/"+password, func(t *testing.T) { require.NoError(t, validatePassword(password)) })
	}
	require.True(t, validateEmail("owner@example.com"))
}
