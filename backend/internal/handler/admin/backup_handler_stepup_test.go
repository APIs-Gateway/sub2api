//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type backupStepUpRepoStub struct {
	service.SettingRepository
	enabled bool
}

func (s backupStepUpRepoStub) GetValue(context.Context, string) (string, error) {
	if s.enabled {
		return "true", nil
	}
	return "false", nil
}

func TestBackupSensitiveOperationsRequireStepUpWhenEnabled(t *testing.T) {
	settingService := service.NewSettingService(&backupStepUpRepoStub{enabled: true}, &config.Config{})
	h := NewBackupHandler(nil, nil)
	h.SetStepUpDeps(nil, settingService)

	operations := []func(*gin.Context){
		h.UpdateS3Config,
		h.CreateBackup,
		h.GetDownloadURL,
		h.RestoreBackup,
	}
	for _, operation := range operations {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/backups/sensitive", nil)
		operation(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Contains(t, rec.Body.String(), "UNAUTHORIZED")
	}
}
