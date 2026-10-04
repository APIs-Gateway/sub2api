-- W6-M1：模型目录（价格与模型配置重构，设计文档 2.2 节）。
--
-- 只建新表，不改任何现有表，也没有任何读取方：计费、调度、准入都不读它。
-- 种子数据不在迁移里：迁移在启动时执行，而种子要扫 usage_logs，所以由管理员在部署后
-- 手动执行 `model-catalog seed`（默认 dry-run）。
--
-- 语义（给后续 PR 的读取方）：
--   status = draft    所有分组关闭；active 按单元格与分组准入模式判定；retired 新请求全部挡。
--   未登记的模型（不在本表里）视同 active，只有显式 draft / retired 才挡（S-3、Q2）。
--   aliases 只放机器路由别名（拼写变体）；口语别名归 W5 的 entity_aliases。
--   reference_model 只用于激活对话框里「照抄某个模型的勾选」快捷按钮的默认选项（Q11）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS model_catalog (
    id               BIGSERIAL PRIMARY KEY,
    model_key        VARCHAR(200) NOT NULL,
    platform         VARCHAR(32)  NOT NULL,
    display_name     VARCHAR(200) NOT NULL DEFAULT '',
    aliases          TEXT[]       NOT NULL DEFAULT '{}',
    reference_model  VARCHAR(200),
    status           VARCHAR(16)  NOT NULL DEFAULT 'draft',
    note             TEXT         NOT NULL DEFAULT '',
    created_by       BIGINT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT model_catalog_status_chk CHECK (status IN ('draft', 'active', 'retired')),
    CONSTRAINT model_catalog_uq UNIQUE (platform, model_key)
);

CREATE INDEX IF NOT EXISTS idx_model_catalog_aliases ON model_catalog USING GIN (aliases);
