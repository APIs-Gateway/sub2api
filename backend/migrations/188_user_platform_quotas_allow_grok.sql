-- 把 user_platform_quotas 的 platform 白名单对齐到 service.AllowedQuotaPlatforms。
--
-- grok 早就是合法的 quota 平台：service.AllowedQuotaPlatforms 和 ent schema 的
-- Validate 都列了它，后台也因此允许把 grok 写进 default_platform_quotas。但 142
-- 建表时的 CHECK 约束漏掉了 grok，三处白名单只同步了两处。
--
-- 这个缺口在注册路径上是致命的，原因不止「插入失败」这么简单：注册时写这批快照的
-- BulkInsertInitial 跑在注册事务内，PostgreSQL 一旦有语句违反约束就会把整个事务标记为
-- aborted，之后的语句一律被拒。调用方写的是 fail-open（只记一条 warning 就继续），
-- 而在事务里这个 fail-open 是假的——紧随其后的「初始化返利档案」和「绑定邀请人」
-- 全部失败，OAuth 注册最终返回 500，邀请关系一条都建不起来。
--
-- 生产库（free.hiyo.top）已在 2026-08-28 手工执行过同样的 DROP/ADD 来紧急止血，
-- 这里补成正式迁移，让其它环境和后续重建保持一致。DROP ... IF EXISTS 保证可重复执行。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok'));
