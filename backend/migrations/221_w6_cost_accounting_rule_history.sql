-- W6 PR4b-2b-2 跟进：成本核算规则的写入历史（审查非阻塞第 9 条）。
--
-- 规则行本身不记操作人，删除之后什么都不剩；通用 admin 审计日志是异步的，队列满会丢。
-- 这里在写规则的同一个事务里追加一行历史：谁、对哪个分组的哪条规则、做了什么、改前改后的完整内容（规则行加价格行）。
-- 只建新表，不改任何现有表；rule_id 不加外键（删除规则之后历史要留着）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS cost_accounting_rule_history (
    id              BIGSERIAL    PRIMARY KEY,
    rule_id         BIGINT       NOT NULL,
    scope_group_id  BIGINT       NOT NULL,
    action          VARCHAR(16)  NOT NULL,
    operator_id     BIGINT       NOT NULL,
    group_revision  BIGINT       NOT NULL,
    before_state    JSONB,
    after_state     JSONB,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT carh_action_chk CHECK (action IN ('create', 'update', 'delete'))
);

CREATE INDEX IF NOT EXISTS idx_carh_group ON cost_accounting_rule_history (scope_group_id, id);
CREATE INDEX IF NOT EXISTS idx_carh_rule ON cost_accounting_rule_history (rule_id, id);
