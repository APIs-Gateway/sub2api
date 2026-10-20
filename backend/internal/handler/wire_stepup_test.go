//go:build unit

package handler

import "testing"

func TestProvideAdminSecurityHandlersWiresStepUpDependencies(t *testing.T) {
	if got := ProvideAdminSettingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil); got == nil {
		t.Fatal("expected setting handler")
	}
	if got := ProvideAdminUserHandler(nil, nil, nil, nil, nil, nil, nil); got == nil {
		t.Fatal("expected user handler")
	}
	if got := ProvideAdminBackupHandler(nil, nil, nil, nil); got == nil {
		t.Fatal("expected backup handler")
	}
}
