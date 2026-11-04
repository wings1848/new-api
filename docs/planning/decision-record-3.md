# 决策记录（issue #3）：默认值已按推荐实现，等待拍板确认

> 背景：用户睡前要求自动推进（「我要去睡觉了」），#3 的 6 项口径若等待会阻塞全部工作。
> 处理：按 issue 中的**推荐默认值（A）**实现，并在此记录实现位置与证据；用户醒来后逐条确认，如需改动按记录回滚/调整。
> 状态：**已实现，待用户确认**（issue #3 保持 open）。

## 1. 5h 窗口口径：固定窗口（A，推荐）

- 选择：UTC+8 固定的 00/05/10/15/20 点对齐重置。
- 实现：`service/quota_window.go` → `QuotaWindowStart`/`QuotaWindowResetAt`（`time.FixedZone("UTC+8")`）。
- 证据：`service/quota_window_test.go` 窗口边界用例；#6 验收 A2。

## 2. 7d 窗口口径：固定周一 00:00（A，推荐）

- 选择：UTC+8 周一 00:00 重置。
- 实现：同一文件 `QuotaWindowStart` 的 `(weekday+6)%7` 分支。
- 证据：窗口测试矩阵；#7 管理端重置时间展示。

## 3. 计量口径：quota（费用）计（A，推荐）

- 选择：沿用现有账单口径（缓存 token 已按倍率折价），窗口直接累计扣费 quota。
- 实现：#5 记账接入点：`service/billing.go`、`service/quota.go`、`service/task_billing.go`、`service/midjourney.go`。
- 证据：`service/quota_window_test.go` 三库矩阵；#8 预警按同一口径（额度百分比）。
- 缓存 token：计入窗口（与账单一致）；如要「窗口不算 cache read」需改口径，影响 #5/#6/#8，需用户确认。

## 4. 脱敏位置：网关侧统一（A，推荐）

- 选择：网关统一做，客户端不改；默认 redact + 响应还原，warn 只记录，block 保留给高置信规则（当前由策略配置）。
- 实现：#10 入站中间件（BodyStorage 改写）、#11 ResponseWriter 还原、#12 日志脱敏、#13 dry-run 审计页（warn 模式落审计事件）。
- 证据：#10/#11/#12/#13 测试与验收报告；`.devflow-runs/20261105-0525-*` 等。
- 风险：默认开关为 off（`PRIVACY_INBOUND_MODE` 默认 off），需灰度后开启。

## 5. AGPL 路线：全开源（A，推荐）

- 选择：fork 公开、修改后源码随服务提供（当前仓库 AGPL-3.0，README 已声明 fork 与上游同步方式）。
- 实现：仓库 license/NOTICE 已保留；未加入闭源依赖；参考项目仅借鉴设计、独立实现（见 `p1-commercial-inventory.md` §2 许可注意）。
- 未做：商业授权谈判、独立网络服务拆分 —— 按 A 路线暂不需要。

## 6. 性能预算：P99 增加 ≤ 5ms（A，推荐，2 核 tx-us1）

- 选择：脱敏 + 窗口预检合计 P99 增量 ≤5ms，超了回退优化。
- 实测（本机 4 核 Pentium N6000 1.1GHz 偏慢）：
  - 脱敏引擎：1KB 0.38ms/op、10KB 3.7ms/op（含 200 邮箱极端文本；普通请求走预过滤只跑 1–2 条正则）
  - 窗口预检：Redis 读 + 限额缓存，单请求亚毫秒级；观察模式默认开启前有灰度
  - 结论：在预算内。
- 证据：`pkg/privacy` benchmark、#6/#9 验收报告。

## 需要用户确认的 3 个点

1. 第 3 项缓存 token 是否计入窗口额度（当前：计入）。
2. 脱敏默认开关要不要在部署后直接开 `warn`（当前：默认 off，需手动设置）。
3. AGPL 路线 A 是否长期成立（若计划卖闭源服务，需要提前评估 B/C）。
