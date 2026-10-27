-- W6-M5：LiteLLM 价格固定快照与待批准差异（价格与模型配置重构，设计文档 2.6 节，PR9）。
--
-- 只建新表，不改任何现有表。默认模式 pricing_snapshot_mode = auto 时没有任何读取方：
-- 计费照旧读 data_dir 下的价格文件与远程同步，不碰这两张表。
--
-- pricing_snapshots：
--   一行是一份完整的 LiteLLM 价格 JSON（payload_gz，gzip），不可变。
--   status = active      生效快照，账单只读它；部分唯一索引保证同时最多一行。
--   status = superseded  被替换下来的旧生效快照（回滚就是把它重新置 active）。
--   status = candidate   拉取到的候选，不影响账单；按 content_sha256 去重，同一内容不重复保存。
--   status = rejected    被拒绝的候选。
--   source = bootstrap   启动引导：把线上此刻正在计费的价格文件原样导入。
--   source = remote      从远程拉取的候选。
--   source = merged      批准产生的快照 = 基线快照 + 被批准的差异。
--   切换生效快照在同一个事务里先把旧行置 superseded，再把新行置 active。
--
-- pricing_snapshot_diffs：候选对基线的逐模型差异与批准决定，随候选快照级联删除。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS pricing_snapshots (
    id                    BIGSERIAL    PRIMARY KEY,
    label                 VARCHAR(64)  NOT NULL,
    source                VARCHAR(12)  NOT NULL,
    source_url            TEXT         NOT NULL DEFAULT '',
    content_sha256        CHAR(64)     NOT NULL,
    model_count           INT          NOT NULL,
    payload_gz            BYTEA        NOT NULL,
    parent_snapshot_id    BIGINT,
    candidate_snapshot_id BIGINT,
    status                VARCHAR(12)  NOT NULL,
    fetched_by            BIGINT,
    fetched_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    approved_by           BIGINT,
    approved_at           TIMESTAMPTZ,
    change_set_id         BIGINT,
    note                  TEXT         NOT NULL DEFAULT '',
    CONSTRAINT pricing_snapshots_source_chk CHECK (source IN ('bootstrap', 'remote', 'merged')),
    CONSTRAINT pricing_snapshots_status_chk CHECK (status IN ('candidate', 'active', 'superseded', 'rejected'))
);

-- 同时只有一个生效快照：生效指针就是 status = 'active' 的那一行，不另存 settings 键。
CREATE UNIQUE INDEX IF NOT EXISTS uq_pricing_snapshots_one_active
    ON pricing_snapshots ((status)) WHERE status = 'active';

-- 每日自动拉取的候选按内容哈希去重：同一内容的候选最多一份。
CREATE UNIQUE INDEX IF NOT EXISTS uq_pricing_snapshots_candidate_sha
    ON pricing_snapshots (content_sha256) WHERE status = 'candidate';

CREATE INDEX IF NOT EXISTS idx_pricing_snapshots_status_fetched
    ON pricing_snapshots (status, fetched_at);

CREATE TABLE IF NOT EXISTS pricing_snapshot_diffs (
    snapshot_id      BIGINT       NOT NULL REFERENCES pricing_snapshots(id) ON DELETE CASCADE,
    base_snapshot_id BIGINT       NOT NULL,
    model_key        VARCHAR(200) NOT NULL,
    change_type      VARCHAR(10)  NOT NULL,
    old_price        JSONB,
    new_price        JSONB,
    changed_fields   TEXT[]       NOT NULL DEFAULT '{}',
    decision         VARCHAR(8),
    PRIMARY KEY (snapshot_id, model_key),
    CONSTRAINT pricing_snapshot_diffs_type_chk CHECK (change_type IN ('added', 'removed', 'changed')),
    CONSTRAINT pricing_snapshot_diffs_decision_chk CHECK (decision IS NULL OR decision IN ('approve', 'hold'))
);
