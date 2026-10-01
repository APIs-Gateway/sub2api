-- Key 级分组回退链：每把 API Key 的兜底分组项。
--
-- 「主分组」仍是 api_keys.group_id，本表只存兜底项，不存主分组。
--   source    = 'user'  用户自己配置的回退链
--             = 'admin' 管理员配置的隐藏链（用户端接口一律不返回）
--   placement = 'tail'  接在主分组之后
--             = 'head'  排在主分组之前（仅 admin 允许）
--   platform  = 该链所属平台（现阶段 = 主分组平台），写入时由服务层校验与 groups.platform 一致
--
-- 同一 source 内同一分组只能出现一次；跨 source 允许重复：用户提交的分组恰好已在隐藏链里时
-- 不能报错，否则等于告诉用户「这把 Key 有隐藏链」。跨 source 的重复由运行时去重处理。
--
-- 主分组不得出现在链里、不跨平台这两条无法在数据库层 CHECK，由服务层保证。
-- 本特性默认关闭，迁移本身不改变任何现有行为。
-- 带外键的 CREATE TABLE 会在 api_keys / groups 上取 SHARE ROW EXCLUSIVE 锁；等不到锁就失败重试，
-- 不能排队挡住请求路径上对这两张表的写入（照 145、196）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS api_key_group_routes (
    id          BIGSERIAL PRIMARY KEY,
    api_key_id  BIGINT      NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id    BIGINT      NOT NULL REFERENCES groups(id)   ON DELETE CASCADE,
    platform    VARCHAR(32) NOT NULL,
    source      VARCHAR(16) NOT NULL DEFAULT 'user',
    placement   VARCHAR(8)  NOT NULL DEFAULT 'tail',
    position    INT         NOT NULL,
    note        VARCHAR(200),
    created_by  BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_agr_source     CHECK (source IN ('user', 'admin')),
    CONSTRAINT chk_agr_placement  CHECK (placement IN ('tail', 'head')),
    CONSTRAINT chk_agr_head_admin CHECK (placement = 'tail' OR source = 'admin'),
    CONSTRAINT chk_agr_position   CHECK (position >= 0 AND position < 16),
    CONSTRAINT uq_agr_key_source_group UNIQUE (api_key_id, source, group_id),
    CONSTRAINT uq_agr_key_pos     UNIQUE (api_key_id, platform, source, placement, position)
);

CREATE INDEX IF NOT EXISTS idx_agr_group_id ON api_key_group_routes (group_id);
