# 交接:分组(Group)与订阅套餐(Subscription)语义重合问题

> 写给接手的另一个 Claude。这是**问题说明 + 代码证据**,不是既定方案。请先按下面的 file:line 复核代码,再决定怎么改。所有行号基于 commit `2fd9f7c5`(main / 已部署的 1.3.0),改动前请自行 `git grep` 复核,代码会漂。

## TL;DR(用户的原话痛点)

> "订阅配额(分配余额的套餐地方)和正常的选择使用分组的地方两个没有区分开,导致我定制套餐和选择分组的地方重合。"

根因:**`groups` 这一张表/一个 `Group` 实体,同时承担了两种毫不相干的角色,只靠一个 `subscription_type` 枚举字段(`standard` / `subscription`)区分。** 管理员要"定制一个套餐"时,被迫去"建一个分组",于是套餐和路由分组在同一个「分组管理」tab、同一张表里混在一起。

## 两种被混在一起的角色

| 角色 | 本质 | 用 `subscription_type` 的取值 |
|---|---|---|
| **A. 路由渠道** | 一组上游账号的池子,用户给 API Key 选它、流量经它转发 | `standard` |
| **B. 套餐/配额载体** | 一个"计划模板",burn-down 模型下唯一有意义的载荷是 `daily_limit_usd`(=套餐日额 D)和 `default_validity_days` | `subscription` |

关键:**角色 B 在 burn-down 模型下根本不参与路由**。订阅金额发放进用户余额后,是**经由角色 A 的标准分组消费**的(见下方 `api_key_service.go:318-327` 注释)。所以角色 B 的分组只是个"套餐定义壳",却被迫长成一个完整的 `Group`。

## 数据模型证据

### `Group` 实体被严重重载 —— `backend/ent/schema/group.go` (`Group.Fields()`)
同一个实体上既有**路由字段**又有**套餐字段**:
- 路由相关(角色 A):`platform`、`rate_multiplier`、`model_routing`/`model_routing_enabled`、`fallback_group_id`、`fallback_group_id_on_invalid_request`、`stable_priority_fallback_group_id`、`supported_model_scopes`、`messages_dispatch_*`、`rpm_limit`、`claude_code_only` …,以及边 `account_groups`(挂上游账号)、`api_keys`、`user_allowed_groups`。
- 套餐/配额相关(角色 B,注释标 "Subscription-related fields (added by migration 003)"):`subscription_type`、`daily_limit_usd`、`weekly_limit_usd`、`monthly_limit_usd`、`default_validity_days`。
- 访问控制:`is_exclusive`(公开/专属)。

### 三层都用 `group_id` 串起来
- `backend/ent/schema/subscription_plan.go`:`field.Int64("group_id")` —— 在售套餐(`price`/`validity_days`/`for_sale`)绑定到某个 group。
- `backend/ent/schema/user_subscription.go`:`field.Int64("group_id")` + burn-down 字段 `granted_total_usd` / `daily_amount_usd` / `consumed_usd` / `clawed_usd`。一张订阅卡绑一个 group。
- 链路:`SubscriptionPlan(group_id) → Group → UserSubscription(group_id)`,全部以 `group_id` 为枢轴。

## 行为/计费链路证据

### 套餐日额 D 来自 group —— `backend/internal/service/subscription_service.go:244-263`
```go
if group, err := s.groupRepo.GetByID(ctx, input.GroupID); err == nil && group != nil && group.DailyLimitUSD != nil {
    dailyAmount = *group.DailyLimitUSD          // D 长在 group 上
}
grantedTotal := dailyAmount * float64(validityDays)   // 套餐总额 G = D × 天数
// ... GrantedTotalUSD: grantedTotal, DailyAmountUSD: dailyAmount
```
即:**套餐的"日额"语义被塞进了 `group.daily_limit_usd` 这个原本是"配额上限"语义的字段里**(字段语义二次重载)。

### 订阅型分组不可被用户绑定,余额走标准分组消费 —— `backend/internal/service/api_key_service.go:318-327`
```go
// burn-down 模型下订阅金额已发放到余额、通过标准渠道消费,订阅型分组不再可作为渠道绑定
func (s *APIKeyService) canUserBindGroup(ctx, user, group) bool {
    if group.IsSubscriptionType() { return false }          // 订阅型:一律不可绑定
    return user.CanBindGroup(group.ID, group.IsExclusive)    // 标准型:走 AllowedGroups + IsExclusive
}
```
- 用户可选分组列表 `GetAvailableGroups` —— `api_key_service.go:744-776`,同样 `if group.IsSubscriptionType() { return false }`,**订阅型分组对用户不可见**。
- 前端用户给 Key 选组:`frontend/src/views/user/KeysView.vue`(调 `GET /groups/available`)。

### 计费仍以 (user, group) 为键,且存在"双计费模型"残留 —— `backend/internal/service/billing_cache_service.go`
- `CheckBillingEligibility`(:708)里 `isSubscriptionMode := group != nil && group.IsSubscriptionType() && subscription != nil`(:718)—— 这是**旧的"按组配额"模型**:请求经过订阅型 group,按 `(userID, groupID)` 查订阅、用 group 的 daily/weekly/monthly 限额。
- 另一边 burn-down 是**用户级聚合**:`lockedSubscriptionBalance(ctx, user.ID)`(:854/:872)汇总该用户全部活跃 burn-down 卡。
- 前端 `SubscriptionsView.vue` 也有"legacy quota model 显示分支"(当 `!daily_amount_usd` 时回退展示 `group.daily/weekly/monthly_limit_usd`)。
- **结论:系统同时残留"按组配额(旧)"与"按用户余额 burn-down(新)"两套模型**,都挂在 group 上,这是混乱的另一来源。

### 前端两个 tab 的现状
- 「分组管理」`frontend/src/views/admin/GroupsView.vue`:同一个建组/编辑表单里,用 `subscription_type` 做**条件显隐**——`=== 'subscription'` 时显示 daily/weekly/monthly 限额段;`!== 'subscription'` 时显示 `is_exclusive`、路由 fallback 等。但底层是同一条 `groups` 记录,字段都在。
- 「订阅管理」`frontend/src/views/admin/SubscriptionsView.vue`:分配订阅时必须从下拉选一个 group,且过滤 `subscription_type === 'subscription' && status === 'active'`;只填 `validity_days`,日额 D 隐式继承自所选 group 的 `daily_limit_usd`。

## 重合到底"重"在哪(管理员视角)

1. **同表同 tab**:要做一个"定制套餐"= 必须新建一个 `subscription_type='subscription'` 的 **Group**,它和真正的路由渠道混在同一个「分组管理」列表里。
2. **套餐壳被迫携带一堆无意义的路由字段**:`rate_multiplier`、`model_routing`、`fallback_*`、`account_groups`、`rpm_limit`、`supported_model_scopes`…… 对一个纯余额套餐毫无意义,只是被表单条件隐藏,记录里仍存在。
3. **字段语义被二次重载**:`daily_limit_usd`(原"配额上限")被当成 burn-down 的"日发放额 D"。
4. **两套不相干的 group 集合都叫"分组"**:订阅管理只认订阅型 group,用户选组只认标准型 group,管理员却要在同一个"分组"概念下维护两类完全不同的东西。
5. **三层 group_id 串联 + 双计费模型残留**,进一步加深"分不清哪个是套餐、哪个是渠道"的体感。

## 给接手者的待决策方向(非定论,请读完代码再判断)

需要先和用户确认**目标形态**,再选路径。可能的方向(按侵入度排序,各有迁移成本):

- **方向 1(最轻,UI/概念层):** 不动表结构,但在「分组管理」里把订阅型分组**单独成一个 tab / 列表**(按 `subscription_type` 物理拆分视图),并把套餐相关字段从"分组表单"里抽到"套餐表单",路由字段对订阅型彻底隐藏且建组时不写。降低体感重合,但底层仍是一张表。
- **方向 2(中,模型分离):** 把"套餐定义"从 `Group` 抽出,落到 `SubscriptionPlan`(或新建 `Plan`)实体——日额 D、validity、features 都归套餐;`UserSubscription` 直接引用套餐而非 group;burn-down 余额彻底用户级、与具体 group 解耦。`Group` 回归"纯路由渠道"。涉及迁移 + 计费链路改造(移除 `isSubscriptionMode` 按组路径,统一到用户级余额)。
- **方向 3(重,清理双模型):** 在方向 2 基础上,彻底下线旧的"按组配额(daily/weekly/monthly_limit)"计费分支,只保留 burn-down,删除/迁移 `group.daily/weekly/monthly_limit_usd` 与相关前端 legacy 分支。

**迁移注意:** `groups`/`user_subscriptions` 都是软删 + 部分唯一索引(`WHERE deleted_at IS NULL`,见 016 迁移);线上 693 用户 / 19 组 / 3 channel_monitor,有真实在用订阅。任何拆表/改键都要写数据迁移并保证幂等(项目惯例:`backend/migrations/NNN_*.sql`,`//go:embed *.sql` 启动自动跑)。

## 复核入口清单(给接手者一键定位)
- `backend/ent/schema/group.go` · `subscription_plan.go` · `user_subscription.go`
- `backend/internal/service/subscription_service.go:244-263`(授予)
- `backend/internal/service/api_key_service.go:318-327` · `:744-776`(绑定/可见过滤)
- `backend/internal/service/billing_cache_service.go:708-721` · `:854-872`(计费双模型)
- `frontend/src/views/admin/GroupsView.vue`(条件显隐表单) · `SubscriptionsView.vue`(订阅分配 + legacy 回退) · `frontend/src/views/user/KeysView.vue`(用户选组)
