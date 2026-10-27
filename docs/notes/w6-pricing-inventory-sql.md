# W6 价格与模型配置重构：只读盘点 SQL

W6 把渠道里的价格、映射、功能开关、账号成本规则派生到新的矩阵表（`model_catalog`、`group_model_config`、`model_group_prices`、`cost_accounting_rules`）。派生之前、回放之前，需要先看清生产数据的形状。本文只放这些盘点用的 SQL 和各自回答的问题，**不放任何生产结果**：结果会过期，每次复跑都应当重新看。

## 怎么跑

- 全部是只读查询。每条放进只读事务并设超时：

  ```sql
  BEGIN READ ONLY;
  SET LOCAL statement_timeout = '60s';
  SET LOCAL lock_timeout = '2s';
  -- 逐条执行下面的查询
  ROLLBACK;
  ```

  也可以用会话级设置（`default_transaction_read_only = on`），效果相同。
- 第 7、8 条会扫 `usage_logs`。先 `EXPLAIN`（不带 `ANALYZE`）确认走 `created_at` 索引的范围扫描，再选低峰执行。
- 表名、列名以执行当天为准：复跑前用 `\d channels`、`\d channel_model_pricing`、`\d channel_pricing_intervals`、`\d channel_account_stats_pricing_rules`、`\d usage_logs` 看一眼，确认没有新迁移改过它们。
- 数据库有多套（例如不同站点各一份）时，每套各跑一遍，结果分开记录。
- 模型目录的种子不在这里：它是部署后手动执行的服务端子命令 `model-catalog seed`（默认 dry-run，见该命令的 `--help`）。

## 查询清单

| 编号 | 回答什么问题 | 决定什么 |
| --- | --- | --- |
| 1、2 | 渠道、分组的规模，停用情况，哪些勾了账号成本 | 派生范围；回放时选哪些分组 |
| 3、6 | 通配符定价的使用情况；同一渠道里的重复条目 | 去重规则（精确名取最后一个、通配符前缀取第一个）是否有实际命中 |
| 4 | 三个功能开关（`web_search_emulation`、`bedrock_cc_compat`、`codex_image_generation_bridge`）的真实形状 | 功能开关的派生值 |
| 5 | 互为前缀且目标不同的通配符映射；只差大小写的精确映射 | 映射确定性规则（精确名优先、前缀长者优先）的线上影响 |
| 7、8 | 近 30 天用量规模；无价用量（计费为零）的基线 | 回放规模；无价计费指标的基线 |
| A1 | 启用渠道里价格全空的条目 | 价格全空的 token 条目要不要派生成空 `custom`（分情况派生）的暴露面 |
| A2 | 账号成本规则按分组各存一份之后，会新生效的组合 | 成本规则的影响量化 |

## SQL

```sql
-- 1. 渠道概况：数量、停用数、是否开了限制、是否勾了账号成本
SELECT status, restrict_models, apply_pricing_to_account_stats, COUNT(*) FROM channels GROUP BY 1,2,3;

-- 2. 渠道与分组：每个渠道关联的分组、平台。过滤软删分组（groups 有 deleted_at），带渠道与分组状态
SELECT cg.channel_id, c.status AS channel_status, cg.group_id, g.name, g.platform, g.status AS group_status, g.rate_multiplier
FROM channel_groups cg
JOIN groups g ON g.id = cg.group_id AND g.deleted_at IS NULL
JOIN channels c ON c.id = cg.channel_id
ORDER BY cg.channel_id, cg.group_id;

-- 2 补：挂在渠道上的软删分组。非 0 行时，派生要显式按「无渠道」处理它们
SELECT cg.channel_id, cg.group_id, g.name, g.deleted_at
FROM channel_groups cg JOIN groups g ON g.id = cg.group_id WHERE g.deleted_at IS NOT NULL;

-- 3. 通配符定价使用情况
SELECT channel_id, platform, models FROM channel_model_pricing WHERE models::text LIKE '%*%';

-- 4. 各功能开关的实际形状
SELECT id, status,
       jsonb_typeof(features_config -> 'web_search_emulation')          AS wse,
       jsonb_typeof(features_config -> 'bedrock_cc_compat')             AS bedrock,
       jsonb_typeof(features_config -> 'codex_image_generation_bridge') AS codex
FROM channels WHERE features_config <> '{}'::jsonb;

-- 5. 互为前缀且目标不同的通配符映射（旧实现里顺序不确定，现在是精确优先、前缀长者优先）
WITH m AS (
  SELECT c.id AS channel_id, p.key AS platform, e.key AS src, e.value AS dst,
         lower(left(e.key, length(e.key) - 1)) AS prefix
  FROM channels c
  CROSS JOIN LATERAL jsonb_each(c.model_mapping) AS p
  CROSS JOIN LATERAL jsonb_each_text(CASE WHEN jsonb_typeof(p.value) = 'object' THEN p.value ELSE '{}'::jsonb END) AS e
  WHERE c.status = 'active' AND jsonb_typeof(c.model_mapping) = 'object' AND right(e.key, 1) = '*'
)
SELECT a.channel_id, a.platform, a.src, a.dst, b.src AS other_src, b.dst AS other_dst
FROM m a JOIN m b
  ON a.channel_id = b.channel_id AND a.platform = b.platform AND a.src < b.src
 AND (starts_with(a.prefix, b.prefix) OR starts_with(b.prefix, a.prefix))
 AND a.dst <> b.dst;

-- 5 补：只差大小写的精确映射
SELECT c.id AS channel_id, p.key AS platform, lower(e.key) AS src_lower,
       array_agg(e.key) AS srcs, array_agg(e.value) AS dsts
FROM channels c
CROSS JOIN LATERAL jsonb_each(c.model_mapping) AS p
CROSS JOIN LATERAL jsonb_each_text(CASE WHEN jsonb_typeof(p.value) = 'object' THEN p.value ELSE '{}'::jsonb END) AS e
WHERE c.status = 'active' AND jsonb_typeof(c.model_mapping) = 'object' AND right(e.key, 1) <> '*'
GROUP BY 1, 2, 3 HAVING count(DISTINCT e.value) > 1;

-- 6. 同一渠道同一平台同一模型名在多个定价条目中重复
--    名字归一化与 legacy 一致、只看启用渠道、带类型守卫、带数组下标
--    精确名：legacy 取最后一个；通配符前缀：legacy 取第一个
SELECT p.channel_id, p.platform,
       CASE WHEN lower(btrim(m.model)) LIKE 'claude-%'
            THEN replace(lower(btrim(m.model)), '.', '-')
            ELSE lower(btrim(m.model)) END                    AS model_norm,
       right(m.model, 1) = '*'                                 AS is_wildcard,
       count(*)                                                AS cnt,
       array_agg(p.id || ':' || m.ord ORDER BY p.id, m.ord)    AS pricing_id_idx
FROM channel_model_pricing p
JOIN channels c ON c.id = p.channel_id AND c.status = 'active'
CROSS JOIN LATERAL jsonb_array_elements_text(
       CASE WHEN jsonb_typeof(p.models) = 'array' THEN p.models ELSE '[]'::jsonb END
     ) WITH ORDINALITY AS m(model, ord)
GROUP BY 1, 2, 3, 4
HAVING count(*) > 1
ORDER BY 1, 2, 3, 4;

-- 7. 近 30 天 usage_logs 行数（回放规模）。先 EXPLAIN，确认走 created_at 索引
SELECT COUNT(*) FROM usage_logs WHERE created_at >= NOW() - INTERVAL '30 days';

-- 8. 无价用量的回溯基线：计费为零、费率倍数为正、有 token 的行，按（分组、模型）汇总。先 EXPLAIN
SELECT group_id, model, COUNT(*) FROM usage_logs
 WHERE created_at >= NOW() - INTERVAL '30 days'
   AND actual_cost = 0 AND total_cost = 0 AND rate_multiplier > 0
   AND (input_tokens + output_tokens + COALESCE(cache_read_tokens,0) + COALESCE(cache_creation_tokens,0) > 0)
 GROUP BY 1,2 ORDER BY 3 DESC LIMIT 200;

-- A1. 启用渠道里价格全空的条目（主表六个价格字段都为空，且没有任何一个区间带价），并带出 restrict_models 分栏
SELECT c.id AS channel_id, c.name, c.restrict_models, p.id AS pricing_id, p.platform, p.billing_mode,
       jsonb_array_length(p.models) AS n_models
FROM channels c
JOIN channel_model_pricing p ON p.channel_id = c.id
WHERE c.status = 'active'
  AND p.input_price IS NULL AND p.output_price IS NULL
  AND p.cache_write_price IS NULL AND p.cache_read_price IS NULL
  AND p.image_output_price IS NULL AND p.per_request_price IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM channel_pricing_intervals i
    WHERE i.pricing_id = p.id
      AND (i.input_price IS NOT NULL OR i.output_price IS NOT NULL OR i.cache_write_price IS NOT NULL
           OR i.cache_read_price IS NOT NULL OR i.per_request_price IS NOT NULL));

-- A2a. 成本规则里列出的分组不属于规则所在渠道
SELECT r.id AS rule_id, r.channel_id, g.group_id
FROM channel_account_stats_pricing_rules r
CROSS JOIN LATERAL unnest(r.group_ids) AS g(group_id)
WHERE NOT EXISTS (SELECT 1 FROM channel_groups cg
                  WHERE cg.channel_id = r.channel_id AND cg.group_id = g.group_id);

-- A2b. 成本规则里列出的账号还服务其他渠道的分组
SELECT r.id AS rule_id, r.channel_id, a.account_id, cg.channel_id AS other_channel_id, ag.group_id
FROM channel_account_stats_pricing_rules r
CROSS JOIN LATERAL unnest(r.account_ids) AS a(account_id)
JOIN account_groups ag ON ag.account_id = a.account_id
JOIN channel_groups cg ON cg.group_id = ag.group_id AND cg.channel_id <> r.channel_id;
```

## 读结果时注意

- 第 8 条的结果里混有订阅计费的行，它们的零值语义与按量计费不同，汇总无价用量时要把它们分开看。
- A1 的「分情况派生」：价格全空的 token 条目在派生里是否写成空 `custom`，取决于所在分组的准入模式、计费来源、映射、官方价事实等；这条 SQL 只量化暴露面，不替代派生函数。
- 软删分组（第 2 补）不会触发任何外键级联，所以派生与回放选分组时必须显式过滤 `groups.deleted_at IS NULL`。
