-- W6-M2：分组配置、矩阵单元格、单元格历史（价格与模型配置重构，设计文档 2.3、2.4 节）。
--
-- 只建新表，不改任何现有表，也没有任何读取方。三张表都不建外键：groups 是软删除，
-- 软删除不会触发外键级联，所以由应用层在读取时丢弃已删分组的行。
--
-- group_model_config：一个分组一行。行不存在 = 默认值 = 无渠道分组的现状
--   （open、billing_model_source 为 NULL、无映射、无功能开关、account_rate、legacy）。
--   billing_model_source 可空：NULL 表示「无渠道」，legacy 在分组没有渠道时返回空串，
--   Anthropic 网关对空串与 channel_mapped 的处理不同，所以不能把 NULL 折成 channel_mapped（S-1）。
--   features 的形状按开关各自实际生效的形状保留（S-2）。
--
-- model_group_prices：单元格。price_mode 三种互斥：inherit / extra / custom。
--   is_pattern = TRUE 时 model_key 是前缀（不含 '*'），只由迁移派生产生；
--   pattern_order 是通配符单元格的先后顺序，先匹配者优先。
--   source = legacy_derived 的行是渠道的派生缓存，渠道每次保存都幂等重算；
--   分组切到 v2 时同一事务里改为 legacy_frozen。
--
-- model_group_price_history：单元格每次变更的审计行，由后续 PR 的写入路径追加；本 PR 不写。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS group_model_config (
    group_id              BIGINT      PRIMARY KEY,
    access_mode           VARCHAR(12) NOT NULL DEFAULT 'open',
    billing_model_source  VARCHAR(16),
    model_mapping         JSONB       NOT NULL DEFAULT '[]',
    features              JSONB       NOT NULL DEFAULT '{}',
    cost_mode             VARCHAR(20) NOT NULL DEFAULT 'account_rate',
    pricing_stage         VARCHAR(8)  NOT NULL DEFAULT 'legacy',
    stage_changed_at      TIMESTAMPTZ,
    stage_changed_by      BIGINT,
    revision              BIGINT      NOT NULL DEFAULT 1,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT gmc_access_chk CHECK (access_mode IN ('open', 'allowlist')),
    CONSTRAINT gmc_stage_chk  CHECK (pricing_stage IN ('legacy', 'shadow', 'v2')),
    CONSTRAINT gmc_cost_chk   CHECK (cost_mode IN ('account_rate', 'catalog_upstream', 'follow_billing')),
    CONSTRAINT gmc_bms_chk    CHECK (billing_model_source IS NULL OR billing_model_source IN ('requested', 'upstream', 'channel_mapped'))
);

CREATE TABLE IF NOT EXISTS model_group_prices (
    id                BIGSERIAL     PRIMARY KEY,
    group_id          BIGINT        NOT NULL,
    model_key         VARCHAR(200)  NOT NULL,
    is_pattern        BOOLEAN       NOT NULL DEFAULT FALSE,
    pattern_order     INT           NOT NULL DEFAULT 0,
    catalog_model_id  BIGINT,
    open              BOOLEAN       NOT NULL DEFAULT TRUE,
    price_mode        VARCHAR(8)    NOT NULL DEFAULT 'inherit',
    extra_multiplier  NUMERIC(10,6),
    custom_price      JSONB,
    effective_from    TIMESTAMPTZ,
    effective_to      TIMESTAMPTZ,
    source            VARCHAR(20)   NOT NULL DEFAULT 'manual',
    revision          BIGINT        NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    CONSTRAINT mgp_mode_chk   CHECK (price_mode IN ('inherit', 'extra', 'custom')),
    CONSTRAINT mgp_extra_chk  CHECK ((price_mode = 'extra') = (extra_multiplier IS NOT NULL) AND (extra_multiplier IS NULL OR extra_multiplier > 0)),
    CONSTRAINT mgp_custom_chk CHECK ((price_mode = 'custom') = (custom_price IS NOT NULL)),
    CONSTRAINT mgp_window_chk CHECK (effective_to IS NULL OR effective_from IS NULL OR effective_to > effective_from),
    CONSTRAINT mgp_source_chk CHECK (source IN ('manual', 'copied', 'legacy_derived', 'legacy_frozen')),
    CONSTRAINT mgp_uq UNIQUE (group_id, model_key, is_pattern)
);

CREATE TABLE IF NOT EXISTS model_group_price_history (
    id             BIGSERIAL     PRIMARY KEY,
    group_id       BIGINT        NOT NULL,
    model_key      VARCHAR(200)  NOT NULL,
    is_pattern     BOOLEAN       NOT NULL DEFAULT FALSE,
    action         VARCHAR(16)   NOT NULL,
    before_state   JSONB,
    after_state    JSONB,
    operator_id    BIGINT,
    change_set_id  BIGINT,
    created_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    CONSTRAINT mgph_action_chk CHECK (action IN ('create', 'update', 'delete', 'archive'))
);

CREATE INDEX IF NOT EXISTS idx_mgph_cell ON model_group_price_history (group_id, model_key, created_at);
