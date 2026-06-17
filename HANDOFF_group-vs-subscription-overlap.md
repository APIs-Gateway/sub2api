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

---

# 实施方案 v1 —— 方向2(模型分离)+ 保留公开在售套餐

> 用户已拍板:**方向2(把套餐定义从 Group 抽到 SubscriptionPlan,UserSubscription 改引 plan,Group 回归纯路由)** + **保留 SubscriptionPlan 公开购买**。**明确不做方向3**(旧「按组配额」legacy 计费分支保留不动,本期只「不再喂它」)。

## 关键利好(让本期改造面比预想小很多)

调研确认 burn-down 的**主消费账本**已和 `group_id` 解耦,但**有一条 legacy writer 仍在热路径上硬连 group**,这是本期最关键的约束:

- `daily_amount_usd`(D)在**开通时**就从 `group.daily_limit_usd` **快照**写进了 `user_subscriptions`(见 `142_subscription_burndown_model.sql` 字段注释)。
- burn-down 主消费 `consumed_usd` 由 `usage_billing_repo.go:211`(`SET consumed_usd = consumed_usd + $1`)写,**不 join groups**;每日清扣(`subscription_clawback_service.go` / `ClawbackSubscription`)、到期没收(`ForfeitExpiredSubscriptions`)、用户级余额聚合(`lockedSubscriptionBalance(userID)`)同样**不读 group**。
- ⚠️ **但** `gateway_service.go:8814` 在**同一条计费热路径**上还调 `userSubRepo.IncrementUsage(p.Subscription.ID, cost)`,写 legacy `daily/weekly/monthly_usage_usd`,其 SQL **硬 join groups**:`... FROM groups g WHERE us.group_id = g.id AND g.deleted_at IS NULL`(`user_subscription_repo.go:369-389`)。**group_id 一旦变 NULL 或 group 被软删,IncrementUsage 每请求 `affected=0` 返回 `ErrSubscriptionNotFound`,线上扣费报错。** → 因此**让 `user_subscriptions.group_id` 可空是真·阻塞项**:必须先(a)改 IncrementUsage 不再硬连 groups(改 LEFT JOIN / 退役该 legacy writer),或(b)把"group_id 可空"推到后期,在此之前 group_id 始终非空且照写。本方案采用 (b)。
- 迁移 **142 已 `DROP` 掉 `(user_id, group_id)` 部分唯一索引**(`142:24`),burn-down 有意允许同用户同套餐**叠加多卡**。→ 本期**无需**新建 `(user_id, plan_id)` 唯一约束。
- `AssignOrExtendSubscription` 在 burn-down 下「始终为新建」(`subscription_service.go:201`),无合并/续期语义。

**结论:`group_id` 在订阅侧有 5 个真实用途**:①开通时取 D(`subscription_service.go:244`);②**legacy `IncrementUsage` 在计费热路径硬连 group**(`gateway_service.go:8814`,本期保留不动 → group_id 必须保持非空);③旧 legacy 分支 `isSubscriptionMode`(保留不动);④展示 group 名/platform;⑤`(user_id,group_id)` 普通查询索引。本期把 ①④ 切到 plan、加 `plan_id`、改三条发放入口即可见效,**而 group_id 的可空化/解绑要等 ② 处理完,放到后期相位**。

## 目标数据模型(= **最终态 / Phase 3 目标**)

> ⚠️ 本节描述改造**完成后**的形态。其中"group_id 改可空 / FK 改 `SET NULL`"属 **Phase 3**,**Phase 0 不做**——Phase 0 仅加列+回填,`group_id` 两表均保持 `NOT NULL`、FK 保持现状。带 **【Phase 3】** 标记的条目勿在 Phase 0 执行。

**`SubscriptionPlan` = 唯一套餐定义(可售)**,新增字段成为 D 的 source of truth:
- 新增 `daily_amount_usd decimal(20,10)`(套餐日额 D,取代「从 group.daily_limit_usd 推导」)。
- 新增 `platform varchar(50)`(展示/筛选用,取代当前从 group 取 platform;见 `payment_handler.go:71-80`)。
- **【Phase 3】**`group_id` 改为**可空**(`field.Int64("group_id").Optional().Nillable()`):套餐不再必须挂一个订阅型 Group。(保留它仅作可选的「服务渠道范围」提示;burn-down 余额实际经标准分组消费,所以可空不影响计费。)
- 既有 price/original_price/validity_days/validity_unit/features/product_name/**for_sale**/sort_order 全部保留 → 公开购买这条线原样不动。

**`UserSubscription`**:
- 新增 `plan_id bigint`(可空,FK `subscription_plans(id) ON DELETE SET NULL`——套餐是硬删商品配置,删套餐不应级联毁历史订阅)。**Phase 0 即加。**
- **【Phase 3】**`group_id` 保留但改**可空**,外键由当前 `ON DELETE CASCADE` 改为 `ON DELETE SET NULL`(删路由分组不应毁历史订阅)。因 D 已快照,运行时计费不受影响。
- 新增 `index.Fields("plan_id")`。**Phase 0 即加。**

## 迁移(拆成两支,幂等,`//go:embed` 自动跑)

> 下一个号是 **155**(现存最高 154;151/152/153 重号是约定内安全行为,勿改名)。
>
> **关键:可空化/FK 改向必须和加列分开**——加列是纯加性、零风险;放松约束依赖 Phase 2 先把 IncrementUsage 去 group 硬连接 + Go 类型/前端容缺改完,否则会破线上扣费。

### 迁移 A —— `155_subscription_plan_decouple_additive.sql`(Phase 0,**纯加性,不动任何约束**)
全部 `IF NOT EXISTS` / `DO $$` 守护:
1. `subscription_plans` 加列 `daily_amount_usd decimal(20,10) NOT NULL DEFAULT 0`、`platform varchar(50) NOT NULL DEFAULT ''`。`group_id` **保持 NOT NULL 不动**。
2. **回填**:`UPDATE subscription_plans p SET daily_amount_usd = COALESCE(g.daily_limit_usd,0), platform = g.platform FROM groups g WHERE p.group_id = g.id AND p.daily_amount_usd = 0;`
3. `user_subscriptions` 加列 `plan_id bigint`(可空);FK `... REFERENCES subscription_plans(id) ON DELETE SET NULL`(plan_id 是新列,SET NULL 立即可用);`CREATE INDEX IF NOT EXISTS idx_user_subscriptions_plan_id ON user_subscriptions(plan_id);`。`group_id` **保持 NOT NULL、FK 仍 CASCADE 不动**。
4. **回填 plan_id(尽力而为)**:`UPDATE user_subscriptions us SET plan_id = (SELECT p.id FROM subscription_plans p WHERE p.group_id = us.group_id ORDER BY p.id LIMIT 1) WHERE us.plan_id IS NULL AND EXISTS (SELECT 1 FROM subscription_plans p2 WHERE p2.group_id = us.group_id);` —— **管理员当初直接按 group 分配、且该 group 无对应 plan 的存量订阅,plan_id 永久为 NULL**,靠已快照的 `daily_amount_usd` 继续正常工作(可接受;前端须对 `plan_id==null` 容缺)。
5. `redeem_codes` 加列 `plan_id bigint`(**照抄现有 `group_id` 范式**:`Optional().Nillable()` + `ON DELETE SET NULL`,见 `005_schema_parity.sql:27`);`CREATE INDEX IF NOT EXISTS idx_redeem_codes_plan_id ...`。

### 迁移 B —— `NNN_subscription_group_id_nullable.sql`(**Phase 3 才上**,前置:Phase 2 完成)
1. `subscription_plans.group_id` `DROP NOT NULL`。
2. `user_subscriptions.group_id` `DROP NOT NULL`;FK `CASCADE → SET NULL`(drop+recreate,DO 块守护)。

ent schema 同步:迁移 A 对应改 `subscription_plan.go`(加 `daily_amount_usd`/`platform`)、`user_subscription.go`(加 `plan_id` 边/字段/索引)、`redeem_code.go`(加 `plan_id`);迁移 B 才把 `group_id` 改 `Optional().Nillable()`(→ Go 类型 `*int64`)。每改完 `go generate ./ent/...` 重新生成。

## 后端改动清单(按 file:line)

| 位置 | 改动 |
|---|---|
| `service/subscription_service.go:243-246` | `createSubscription` 改从 **plan** 取 D:载入 plan、`dailyAmount = plan.DailyAmountUSD`;写 `sub.PlanID`。 |
| `AssignSubscriptionInput`(同文件) | 增 `PlanID int64`;`GroupID` 降级为可选(兼容/审计)。 |
| `service/subscription_service.go:159` + `handler/admin/subscription_handler.go:42-46,147-153` | `AssignSubscriptionRequest` 增 `plan_id`;校验 plan 存在(替代「校验 group 是订阅型」)。 |
| `service/redeem_code.go` + `redeem_service.go:414-416,477-483` | 订阅型兑换码改带 `plan_id`(替代 `GroupID` 必填校验),发放时传 `PlanID`。 |
| `service/payment_order.go:207-209` | 订单已存 `plan_id`,保留;可不再依赖 `SubscriptionGroupID` 推 D。 |
| `service/payment_fulfillment.go:439` | `doSub` 给 `AssignSubscriptionInput` 传 `PlanID`(而非/兼于 `GroupID`)。 |
| `service/payment_config_plans.go:14-58,123-170` | `validatePlan*`:`group_id` 不再必填;Create/Update 处理 `daily_amount_usd`/`platform`。 |
| `handler/payment_handler.go:56-80,117-124` | 公开套餐列表/详情直接出 `plan.daily_amount_usd` + `plan.platform`(替代 group 派生);对仍挂 group 的旧套餐保留 group 回退。 |

## 前端改动清单

| 文件 | 改动 |
|---|---|
| `views/admin/orders/AdminPaymentPlansView.vue` + `PlanEditDialog.vue` | 套餐表单新增 日额 D / validity / features / platform 字段;group 下拉改为**可选**(或移除),`GroupBadge` 容缺。 |
| `views/admin/SubscriptionsView.vue:70-82,202-212` | 分配对话框改**选套餐(plan)**;列表筛选按 plan(或保留 group 筛选映射到 plan.group);展示 plan 名。 |
| `views/user/SubscriptionsView.vue:43,70` | 展示 plan 名;续期跳转传 `plan_id`。 |
| `components/payment/SubscriptionPlanCard.vue:46-56,138-140` | 直接读 `plan.daily_amount_usd` / `plan.platform`(不再经 group)。 |
| `handler/dto/types.go:572-604` + 前端 types | DTO:`UserSubscription` 增 `PlanID`/`Plan`;plan DTO 增 `daily_amount_usd`/`platform`。 |

## 分期(评审调整版:**可空化推迟到 Phase 3**,Phase 0–2 全程 group_id 非空且照写,上线风险最低)

- **Phase 0**:迁移 A(纯加列 + 回填)+ ent 重生。group_id 两表均保持 NOT NULL,**零行为变化**。
- **Phase 1**:三条发放入口(管理员分配/兑换码/购买)改从 `plan.daily_amount_usd` 取 D、写 `plan_id`;**同时继续照写 group_id**(始终非空,供 legacy 热路径/展示/兜底)。套餐此阶段仍必须挂一个 group。因 D 回填 = group 原值,**行为完全一致**。
- **Phase 2(容缺准备,仍不改可空)**:(a) 把 `IncrementUsage` 的 `FROM groups` 硬连接改掉(LEFT JOIN 或退役该 legacy writer);(b) Go 类型 `group_id int64 → *int64` 贯穿 service model / repo(`SetGroupID`/`WithGroup`/`GroupIDEQ`)/ DTO;(c) 前端对 `plan_id==null`、`group==null` 全面容缺;(d) 公开套餐展示改读 `plan.platform`/`plan.daily_amount_usd`;(e) **定死负数天数兑换码语义**(见下)。
- **Phase 3(真正解耦)**:迁移 B(两表 group_id 可空 + `user_subscriptions.group_id` FK `CASCADE→SET NULL`);套餐可不挂 group;分配 UX 按 plan;`GroupsView` 建组表单去掉订阅型字段 → Group 回归纯路由;后台删分组流程不再级联删订阅。
- **Phase 4(本期不做,= 方向3)**:下线 legacy `isSubscriptionMode` / `GetSubscriptionStatus(user,group)`,删 `group.daily/weekly/monthly_limit_usd` + `subscription_type`,最终可删 `user_subscriptions.group_id`。

## 待执行者复核的风险点 / 待定决策(评审补强)

1. **`IncrementUsage` 热路径硬连 group(Phase 3 前置阻塞)**:`gateway_service.go:8814` 每次扣费调 `userSubRepo.IncrementUsage`,其 SQL `FROM groups g WHERE us.group_id=g.id AND g.deleted_at IS NULL`(`user_subscription_repo.go:369-389`)。group_id 可空/被删 → 每请求 `affected=0` 报错。**Phase 2 必须先改它(LEFT JOIN 或退役该 legacy writer),Phase 3 才能放可空。** 这是整个方案的硬约束。
2. **legacy 分支是否真的不在 burn-down 触发**:`billing_cache_service.go:708-724` `isSubscriptionMode` 要求请求经「订阅型 group」路由,而 burn-down 下订阅金额经**标准分组**消费(`api_key_service.go:318-327` 订阅型 group 不可绑定)。执行者需确认 burn-down 请求永不命中该分支,方可保留不动。
3. **负数天数订阅兑换码语义(必须定死)**:现支持负数缩短/取消"指定 group"的订阅(`redeem_service.go:466-468`「负数天数:缩短订阅,减到 0 则取消」→ `reduceOrCancelSubscription:655` → `ExtendSubscription` 的 `ShortenSubscriptionWithReclaim` burn-down 回收)。改 plan 后多卡并存,"缩哪张"悬空。**三选一**:① 定位到该 plan 最新一张活跃卡(与「始终新建」语义一致,**推荐**);② 缩所有同 plan 卡;③ 禁用负数订阅兑换码。选定后写进文档/UI。
4. **`group_id int64 → *int64` 的爆炸半径(Phase 2)**:改类型前先 `grep -rn "SetGroupID\|WithGroup()\|GroupIDEQ\|\.GroupID\b"` 量化改动面(service model / repo / DTO / 前端 `plan.group_id: number`)。`redeem_codes.group_id` 已是 `*int64`(`Optional().Nillable()`),可作样板。建议走 `*int64` 而非「0=nil」约定(后者易脏)。
5. **存量孤儿订阅**:管理员当初直接按 group 分配、group 无对应 plan 的卡,`plan_id` 永久 NULL —— 前端展示与续期须对 `plan_id == null` 兜底(回退显示 group 名 / 禁用续期或引导新购)。
6. **删分组流程(Phase 3)**:`DeleteGroup`(`admin_service.go:2330`)+ 当前 FK `ON DELETE CASCADE`(`003_subscription.sql:20`)→ **现在删分组会级联删订阅**。改 `SET NULL` 后,还要检查 DeleteGroup 是否有「有活跃订阅则阻塞 / 级联清订阅」的逻辑需放开,目标是"删路由分组不毁历史订阅"。
7. **`plan.platform` 语义(必须写清)**:**仅作展示/筛选**,不构成路由或权限约束——否则日后又变成第三种隐式约束,重蹈 group 覆辙。若将来确需按平台限制可用路由,另起字段另作设计。
8. **plan 的 group_id 留不留**:建议 Phase 3 起留为**可选**「服务范围」提示;若决定彻底不留,一并去掉 plan 表单的 group 选择。

## 验收

`go generate ./ent/...` → `go build ./...` → `go vet ./...` → 定向测试 `subscription_service` / `billing_cache_service` / `payment_*` / `redeem_service`;前端 `vue-tsc` 类型检查。线上 693 用户 / 有真实在用订阅,Phase 0/1 上线后**抽查**几张存量卡的 remaining/consumed 展示与扣费正常。
