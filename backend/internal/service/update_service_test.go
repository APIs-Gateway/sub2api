//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release *GitHubRelease
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	panic("FetchRecentReleases should not be called by UpdateService")
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestParseVersionStripsHyphenatedSuffix(t *testing.T) {
	require.Equal(t, [3]int{0, 1, 183}, parseVersion("v0.1.183-custom"))
	require.Equal(t, [3]int{1, 2, 3}, parseVersion("1.2.3-rc.1"))
	require.Equal(t, [3]int{1, 2, 3}, parseVersion("v1.2.3"))
}

func TestCompareVersionsIgnoresHyphenatedSuffix(t *testing.T) {
	require.Equal(t, 0, compareVersions("v0.1.183-custom", "v0.1.183"))
	require.Equal(t, -1, compareVersions("v0.1.183-custom", "v0.1.184"))
	require.Equal(t, 1, compareVersions("v0.1.184-rc.1", "v0.1.183"))
}

type updateServiceChecksumClientStub struct {
	updateServiceGitHubClientStub
	checksumData []byte
	checksumErr  error
}

func (s *updateServiceChecksumClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return s.checksumData, s.checksumErr
}

func writeChecksumTestArchive(t *testing.T) (string, string) {
	t.Helper()
	content := []byte("fake archive content")
	path := filepath.Join(t.TempDir(), "sub2api_0.2.9_linux_amd64.tar.gz")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	sum := sha256.Sum256(content)
	return path, hex.EncodeToString(sum[:])
}

func TestUpdateServicePerformUpdateRejectsMissingChecksumBeforeDownload(t *testing.T) {
	client := &updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.2.9"}}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.0", "release")
	archive := "sub2api_" + svc.getArchiveName() + ".tar.gz"
	client.release.Assets = []GitHubAsset{{
		Name:               archive,
		BrowserDownloadURL: "https://github.com/Wei-Shaw/sub2api/releases/download/v0.2.9/" + archive,
	}}

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "checksums.txt")
}

func TestUpdateServiceVerifyChecksumRequiresMatchingEntry(t *testing.T) {
	path, hash := writeChecksumTestArchive(t)
	fileName := filepath.Base(path)
	for _, tc := range []struct {
		name         string
		checksumData []byte
		checksumErr  error
		wantError    string
	}{
		{name: "checksum download failed", checksumErr: errors.New("network down"), wantError: "failed to download checksums"},
		{name: "archive not listed", checksumData: []byte(hash + "  other_file.tar.gz\n"), wantError: "checksum not found"},
		{name: "hash mismatch", checksumData: []byte("deadbeef  " + fileName + "\n"), wantError: "checksum mismatch"},
		{name: "hash matches", checksumData: []byte(hash + "  " + fileName + "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &updateServiceChecksumClientStub{checksumData: tc.checksumData, checksumErr: tc.checksumErr}
			svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.0", "release")
			err := svc.verifyChecksum(context.Background(), path, "https://github.com/Wei-Shaw/sub2api/releases/download/v0.2.9/checksums.txt")
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
