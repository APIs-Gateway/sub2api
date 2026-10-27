-- W6-M7：阶段切换的闸门证据与审计（PR7b，设计文档 4.3、4.5、4.6）。
--
-- 只建两张新表，不改任何现有表。两张表没有任何计费、调度、准入读取方：
--   - pricing_replay_evidence 由 `pricing-replay --record` 写入，只在管理员预览、提交「shadow 到 v2」时被读取；
--   - pricing_stage_audit 由阶段切换事务追加，只有管理端读取。
--
-- pricing_replay_evidence：一次回放里一个分组的结果（每次回放每个分组一行，追加不改）。
--   channel_config_hash   回放结束时渠道配置相关全部表的内容摘要（回放开始与结束一致才会记 binding_stable）。
--   derive_revision       回放时该分组按渠道当前配置实时派生的 revision（含所用官方价事实的摘要）。
--   passed                该分组：翻译差异为 0、没有回放错误、绑定稳定、取得了派生 revision。
--   diffs                 [{kind, class, reason, count}]，切换预览按 reason 列出预期差异并估算价格方向。
--   切换时必须同时满足：passed、窗口不短于 30 天、绑定的 revision 与摘要等于「现在」，否则要求重跑。
--
-- pricing_stage_audit：每次阶段变更一行，与阶段变更、审批消耗在同一个事务里写入。
--   kind      advance（向后推进：legacy→shadow、shadow→v2）| rollback（回拨：v2→shadow、v2→legacy、shadow→legacy）。
--   evidence  闸门证据（影子观察、回放、绑定、被接受的差异）或回滚的归档摘要（归档的成本核算行、单元格数）。
--
-- 不建外键：与 W6 其余表一致（groups 是软删除）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS pricing_replay_evidence (
    id                 BIGSERIAL    PRIMARY KEY,
    group_id           BIGINT       NOT NULL,
    recorded_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    window_from        TIMESTAMPTZ  NOT NULL,
    window_to          TIMESTAMPTZ  NOT NULL,
    matrix_source      VARCHAR(8)   NOT NULL,
    passed             BOOLEAN      NOT NULL,
    binding_stable     BOOLEAN      NOT NULL,
    rows_in_window     BIGINT       NOT NULL,
    rows_replayed      BIGINT       NOT NULL,
    rows_errored       BIGINT       NOT NULL,
    translation_diffs  BIGINT       NOT NULL,
    expected_diffs     BIGINT       NOT NULL,
    channel_config_hash VARCHAR(64) NOT NULL,
    derive_revision    VARCHAR(128) NOT NULL,
    matrix_hash        VARCHAR(64)  NOT NULL DEFAULT '',
    pricing_data_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    tool_version       VARCHAR(64)  NOT NULL DEFAULT '',
    diffs              JSONB        NOT NULL DEFAULT '[]',
    CONSTRAINT pre_source_chk CHECK (matrix_source IN ('derived', 'stored')),
    CONSTRAINT pre_window_chk CHECK (window_to > window_from)
);

CREATE INDEX IF NOT EXISTS idx_pricing_replay_evidence_group ON pricing_replay_evidence (group_id, recorded_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS pricing_stage_audit (
    id                      BIGSERIAL    PRIMARY KEY,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    group_id                BIGINT       NOT NULL,
    from_stage              VARCHAR(8)   NOT NULL,
    to_stage                VARCHAR(8)   NOT NULL,
    kind                    VARCHAR(8)   NOT NULL,
    operator_id             BIGINT       NOT NULL,
    interactive             BOOLEAN      NOT NULL DEFAULT FALSE,
    approval_id             BIGINT,
    price_delta             VARCHAR(8)   NOT NULL DEFAULT 'none',
    config_revision_before  BIGINT       NOT NULL,
    config_revision_after   BIGINT       NOT NULL,
    evidence                JSONB        NOT NULL DEFAULT '{}',
    CONSTRAINT psa_stage_chk CHECK (from_stage IN ('legacy', 'shadow', 'v2') AND to_stage IN ('legacy', 'shadow', 'v2')),
    CONSTRAINT psa_kind_chk  CHECK (kind IN ('advance', 'rollback')),
    CONSTRAINT psa_delta_chk CHECK (price_delta IN ('up', 'down', 'none', 'unknown'))
);

CREATE INDEX IF NOT EXISTS idx_pricing_stage_audit_group ON pricing_stage_audit (group_id, created_at DESC, id DESC);
