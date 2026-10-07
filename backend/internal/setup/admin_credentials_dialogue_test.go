package setup

import (
	"bufio"
	"strings"
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestSetupAdminCredentials_DialogueRetriesInvalidInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("not-email\na@b\nName <a@b.com>\n owner@example.com \n"))
	passwords := []string{"1234567", strings.Repeat("x", 73), strings.Repeat("界", 25), "valid-password", "wrong-confirmation", " final-password ", " final-password "}
	var prompts []string
	admin, err := promptAdminCredentials(reader, func(prompt string) string {
		require.NotEmpty(t, passwords, "dialogue must not ask for extra input")
		prompts = append(prompts, prompt)
		value := passwords[0]
		passwords = passwords[1:]
		return value
	})
	require.NoError(t, err)
	require.Empty(t, passwords)
	require.Equal(t, "owner@example.com", admin.Email)
	require.Equal(t, " final-password ", admin.Password, "password whitespace must not be trimmed")
	require.Equal(t, []string{"Admin Password", "Admin Password", "Admin Password", "Admin Password", "Confirm Password", "Admin Password", "Confirm Password"}, prompts)
}

func TestSetupAdminCredentials_DialogueRandomDefaultAndByteLimits(t *testing.T) {
	for _, password := range []string{"12345678", strings.Repeat("x", 72), strings.Repeat("界", 24)} {
		t.Run(password, func(t *testing.T) {
			calls := 0
			admin, err := promptAdminCredentials(bufio.NewReader(strings.NewReader("\n")), func(string) string {
				calls++
				return password
			})
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.Regexp(t, `^admin-[0-9a-f]{12}@sub2api\.local$`, admin.Email)
			require.Equal(t, password, admin.Password)
			require.NoError(t, binding.Validator.ValidateStruct(&struct {
				Email string `binding:"required,email"`
			}{Email: admin.Email}))
		})
	}
}
