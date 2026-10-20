package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTestRedisAcceptsUsernameBeforeConnectionAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body, err := json.Marshal(TestRedisRequest{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "  app-user  ",
	})
	require.NoError(t, err)
	c.Request = httptest.NewRequest(http.MethodPost, "/setup/test-redis", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	testRedis(c)

	// The request reaches Redis connection testing, so the username trim and
	// RedisConfig wiring are exercised without requiring a live Redis server.
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTestRedisRejectsLongUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body, err := json.Marshal(TestRedisRequest{
		Host:     "127.0.0.1",
		Port:     6379,
		Username: strings.Repeat("u", 129),
	})
	require.NoError(t, err)
	c.Request = httptest.NewRequest(http.MethodPost, "/setup/test-redis", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	testRedis(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid Redis username")
}

func TestInstallRejectsLongRedisUsername(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body, err := json.Marshal(InstallRequest{
		Database: DatabaseConfig{
			Host:   "127.0.0.1",
			Port:   5432,
			User:   "postgres",
			DBName: "sub2api",
		},
		Redis: RedisConfig{
			Host:     "127.0.0.1",
			Port:     6379,
			Username: "  " + strings.Repeat("u", 129) + "  ",
		},
		Admin: AdminConfig{
			Email:    "admin@example.com",
			Password: "password123",
		},
	})
	require.NoError(t, err)
	c.Request = httptest.NewRequest(http.MethodPost, "/setup/install", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	install(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid Redis username")
}
