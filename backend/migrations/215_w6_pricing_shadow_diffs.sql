-- W6-M4：影子比对差异样本（PR5，设计文档 2.7、4.4）。
--
-- 占位编号原为 203；这里用 215（203 至 207 没有被使用，208 至 214 已被别的 PR 占用）。
-- 只建一张新表，不改任何现有表。只存差异样本（有上限的采样），不是全量比对记录：
--   - 计数走进程内指标（pricing_shadow_diff_total、pricing_shadow_compared_total），不靠这张表；
--   - 写入端的采样上限：同一（分组、类别、分类、模型）10 分钟内最多一条，全进程每小时最多 300 条；
--   - 保留 14 天，由写入进程的后台协程每小时清理一次（DELETE ... WHERE created_at < now() - 14 days）。
--
-- kind：access | mapping | feature | cost | account_cost。
-- class：translation（渠道到矩阵的翻译差异，切换门槛要求为 0）| expected（v2 新语义带来的预期差异，例如单元格 open=false 的例外）。
-- usage_ref：用量行的 request id，便于对账；没有时为 NULL。
-- legacy_view / v2_view：两边各自的结果（金额与模式、准入结果、映射目标），不含用户信息。
--
-- 不建外键：与 W6 其余表一致（groups 是软删除）。没有任何现有读取方。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS pricing_shadow_diffs (
    id           BIGSERIAL     PRIMARY KEY,
    created_at   TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    group_id     BIGINT        NOT NULL,
    model        VARCHAR(200)  NOT NULL,
    kind         VARCHAR(16)   NOT NULL,
    class        VARCHAR(12)   NOT NULL DEFAULT 'translation',
    usage_ref    VARCHAR(64),
    legacy_view  JSONB         NOT NULL,
    v2_view      JSONB         NOT NULL,
    CONSTRAINT psd_kind_chk  CHECK (kind IN ('access', 'mapping', 'feature', 'cost', 'account_cost')),
    CONSTRAINT psd_class_chk CHECK (class IN ('translation', 'expected'))
);

CREATE INDEX IF NOT EXISTS idx_pricing_shadow_diffs_created ON pricing_shadow_diffs (created_at);
CREATE INDEX IF NOT EXISTS idx_pricing_shadow_diffs_group_created ON pricing_shadow_diffs (group_id, created_at DESC);
