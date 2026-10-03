.PHONY: build build-backend build-frontend dev-db dev-local test test-backend test-frontend test-frontend-critical secret-scan

FRONTEND_CRITICAL_VITEST := \
	src/components/admin/monitor/__tests__/MonitorTemplateApplyPickerDialog.spec.ts \
	src/components/admin/monitor/__tests__/MonitorTemplateApplyPickerDialog.apply.spec.ts \
	src/components/admin/monitor/__tests__/MonitorTemplateManagerDialog.requests.spec.ts \
	src/views/admin/ops/components/__tests__/OpsAlertRulesCard.duration.spec.ts \
	src/components/account/__tests__/BulkEditAccountModal.spec.ts \
	src/components/account/__tests__/CreateAccountModal.modelPreview.spec.ts \
	src/components/account/__tests__/ModelWhitelistSelector.spec.ts \
	src/components/account/__tests__/EditAccountModal.spec.ts \
	src/i18n/__tests__/localeKeyCompleteness.spec.ts \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/components/admin/user/__tests__/UserPlatformQuotaModal.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/RedeemView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/components/user/dashboard/__tests__/CheckinCard.spec.ts \
	src/views/user/__tests__/DashboardView.spec.ts \
	src/__tests__/App.documentTitle.spec.ts \
	src/stores/__tests__/auth.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/components/user/profile/__tests__/ProfileIdentityBindingsSection.spec.ts \
	src/components/user/profile/__tests__/ProfileIdentityBindingsSection.emailDraft.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/views/admin/orders/__tests__/AdminOrdersView.spec.ts \
	src/views/admin/ops/components/__tests__/OpsSettingsDialog.loading.spec.ts \
	src/views/admin/ops/components/__tests__/OpsAlertEventsCard.pagination.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 只启动真实 PostgreSQL/Redis，不构建应用镜像，适合本地热开发。
dev-db:
	@tools/dev_local.sh db

# 真实 PostgreSQL/Redis 在 Docker 中，本机复用 pnpm/go 缓存编译运行应用。
dev-local:
	@tools/dev_local.sh run

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)

secret-scan:
	@python3 tools/secret_scan.py
