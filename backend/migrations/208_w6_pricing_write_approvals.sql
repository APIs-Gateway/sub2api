-- W6-M6：价格写入的过渡审批记录（PR4b-1，设计文档 6.3、附录 S-13）。
--
-- W5（change-set 与审批）落地之前，单元格写入走内置的 PriceWriteGate：先登记一次预览，
-- 写入时在同一个事务里原子地消耗它。消耗后的行就是审批记录：预览人、批准人、计划指纹、
-- 价格方向与前后对比；W5 落地后同一入口改为创建 change-set，本表只留作历史。
--
--   status = previewed  已预览、未写入；过期（expires_at）或指纹对不上都不能被消耗。
--   status = consumed   已被一次写入消耗，approved_by / consumed_at 必填；永不删除。
--   plan_hash           操作、单元格基线、分组基线的 SHA-256，写入请求必须与预览逐字节一致。
--   price_delta         up / down / none / unknown；非 none 的涉价写入要求交互式管理员会话。
--
-- 同时给 model_group_price_history 补一列 approval_id（可空，指向本表的审批记录；不建外键，
-- 与 W6 其余表一致）。两处都是纯新增，没有任何现有读取方。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS pricing_write_approvals (
    id             BIGSERIAL    PRIMARY KEY,
    kind           VARCHAR(24)  NOT NULL,
    status         VARCHAR(12)  NOT NULL DEFAULT 'previewed',
    plan_hash      VARCHAR(64)  NOT NULL,
    touches_price  BOOLEAN      NOT NULL,
    price_delta    VARCHAR(8)   NOT NULL,
    group_ids      BIGINT[]     NOT NULL DEFAULT '{}',
    summary        JSONB        NOT NULL DEFAULT '{}',
    previewed_by   BIGINT       NOT NULL,
    approved_by    BIGINT,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ  NOT NULL,
    consumed_at    TIMESTAMPTZ,
    CONSTRAINT pwa_status_chk   CHECK (status IN ('previewed', 'consumed')),
    CONSTRAINT pwa_delta_chk    CHECK (price_delta IN ('up', 'down', 'none', 'unknown')),
    CONSTRAINT pwa_consumed_chk CHECK ((status = 'consumed') = (consumed_at IS NOT NULL) AND (status = 'consumed') = (approved_by IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_pwa_status_created ON pricing_write_approvals (status, created_at);

ALTER TABLE model_group_price_history ADD COLUMN IF NOT EXISTS approval_id BIGINT;
