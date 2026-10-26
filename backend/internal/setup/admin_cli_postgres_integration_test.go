//go:build integration

package setup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Exercise RunCLI itself in a child process so its real terminal streams never
// replace another test's globals. Each answer is sent only after its prompt:
// promptPassword uses its own reader, so preloading input can lose later lines.
func TestSetupAdminBootstrapPostgres_CLIWizardCancel(t *testing.T) {
	if os.Getenv("SUB2API_SETUP_CLI_CHILD") == "1" {
		require.NoError(t, RunCLI())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("setup_admin_cli"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	pgHost, err := pg.Host(ctx)
	require.NoError(t, err)
	pgPort, err := pg.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	rc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp"),
		}, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rc.Terminate(context.Background())) })
	redisHost, err := rc.Host(ctx)
	require.NoError(t, err)
	redisPort, err := rc.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	childCtx, childCancel := context.WithTimeout(ctx, 45*time.Second)
	defer childCancel()
	args := []string{"-test.run=^TestSetupAdminBootstrapPostgres_CLIWizardCancel$", "-test.v"}
	if profile := os.Getenv("SETUP_CLI_COVERPROFILE"); profile != "" {
		require.True(t, filepath.IsAbs(profile), "child changes directory; coverage destination must be absolute")
		args = append(args, "-test.coverprofile="+profile)
	}
	cmd := exec.CommandContext(childCtx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SUB2API_SETUP_CLI_CHILD=1")
	cmd.Dir = t.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var output strings.Builder
	for _, step := range []struct{ prompt, answer string }{
		{"PostgreSQL Host [", pgHost}, {"PostgreSQL Port [", pgPort.Port()},
		{"PostgreSQL User [", "postgres"}, {"PostgreSQL Password:", "postgres"},
		{"Database Name [", "setup_admin_cli"}, {"SSL Mode [", "disable"},
		{"Redis Host [", redisHost}, {"Redis Port [", redisPort.Port()},
		{"Redis Password (optional):", ""}, {"Redis DB [", "0"},
		{"Enable Redis TLS? [", "n"}, {"Admin Email (login username) [", ""},
		{"Admin Password:", "valid-password"}, {"Confirm Password:", "valid-password"},
		{"Server Port [", "8080"}, {"Proceed with installation? [", "n"},
	} {
		start := output.Len()
		for {
			var b [1]byte
			_, err := io.ReadFull(stdout, b[:])
			require.NoError(t, err, "waiting for %q; output: %s", step.prompt, output.String())
			output.WriteByte(b[0])
			current := output.String()[start:]
			if strings.Contains(current, step.prompt) && strings.HasSuffix(current, ": ") {
				break
			}
		}
		_, err := fmt.Fprintln(stdin, step.answer)
		require.NoError(t, err)
	}
	require.NoError(t, stdin.Close())
	tail, readErr := io.ReadAll(stdout)
	output.Write(tail)
	waitErr := cmd.Wait()
	waited = true
	require.NoError(t, readErr)
	require.NoError(t, waitErr, "wizard output: %s; stderr: %s", output.String(), stderr.String())
	require.Contains(t, output.String(), "Testing database connection... OK")
	require.Contains(t, output.String(), "Testing Redis connection... OK")
	require.Regexp(t, `Admin: admin-[0-9a-f]{12}@sub2api\.local\n`, output.String())
	require.Contains(t, output.String(), "Installation cancelled")
	require.NotContains(t, output.String(), "Installing...")
	entries, err := os.ReadDir(cmd.Dir)
	require.NoError(t, err)
	require.Empty(t, entries, "cancel must not write configuration or installation lock")
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%s user=postgres password=postgres dbname=setup_admin_cli sslmode=disable", pgHost, pgPort.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var usersTables int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='users'`).Scan(&usersTables))
	require.Zero(t, usersTables, "cancel must not migrate or create accounts")
	client := redis.NewClient(&redis.Options{Addr: redisHost + ":" + redisPort.Port()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	size, err := client.DBSize(ctx).Result()
	require.NoError(t, err)
	require.Zero(t, size, "cancel must leave isolated Redis untouched")
}
