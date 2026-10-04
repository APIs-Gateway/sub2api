-- W6-M3：成本核算规则（原「账号成本规则」改名，设计文档 2.5 节）。
--
-- 只建新表，不改任何现有表，也没有任何读取方。
--
-- 规则按分组各存一份（R2-BK-1）：渠道里的每条规则，对渠道内每个分组各派生一行，
-- scope_group_id 是该分组，source_ordinal 是这条规则在渠道内原来的顺序（按 sort_order、id 排名，从 1 起）。
-- 这样分组切到 v2 时可以只冻结自己那一行（legacy_derived 改 legacy_frozen），渠道保存的派生钩子
-- 不会覆盖已冻结的行。命中条件：请求分组 = scope_group_id，并且账号命中（account_ids）或规则内分组命中（group_ids）；
-- 同一分组内按 (sort_order, source_ordinal, id) 排序，source_ordinal 为空（manual）的排在后面。
--
-- 非 manual 的行必须带来源渠道和来源顺序（派生钩子按它们定位、对账）。
-- 价格行随规则行各复制一份，按 rule_id 级联删除；冻结只改规则行的 source，价格行不动。
-- cost_accounting_rule_prices.platform 的宽度取 50，与渠道侧 channel_account_stats_model_pricing.platform 一致
-- （设计文档写的是 32：派生是原样复制，不能因为旧数据超过 32 个字符而让钩子失败）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS cost_accounting_rules (
    id                 BIGSERIAL    PRIMARY KEY,
    name               VARCHAR(100) NOT NULL,
    scope_group_id     BIGINT       NOT NULL,
    source             VARCHAR(16)  NOT NULL DEFAULT 'manual',
    source_channel_id  BIGINT,
    source_ordinal     INT,
    group_ids          BIGINT[]     NOT NULL DEFAULT '{}',
    account_ids        BIGINT[]     NOT NULL DEFAULT '{}',
    sort_order         INT          NOT NULL DEFAULT 0,
    enabled            BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT car_source_chk CHECK (source IN ('legacy_derived', 'legacy_frozen', 'manual')),
    CONSTRAINT car_origin_chk CHECK (source = 'manual' OR (source_channel_id IS NOT NULL AND source_ordinal IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_car_scope ON cost_accounting_rules (scope_group_id, sort_order, source_ordinal, id);
CREATE INDEX IF NOT EXISTS idx_car_source_channel ON cost_accounting_rules (source_channel_id) WHERE source <> 'manual';

CREATE TABLE IF NOT EXISTS cost_accounting_rule_prices (
    id        BIGSERIAL   PRIMARY KEY,
    rule_id   BIGINT      NOT NULL REFERENCES cost_accounting_rules(id) ON DELETE CASCADE,
    platform  VARCHAR(50) NOT NULL DEFAULT '',
    models    JSONB       NOT NULL,
    price     JSONB       NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_carp_rule ON cost_accounting_rule_prices (rule_id);
