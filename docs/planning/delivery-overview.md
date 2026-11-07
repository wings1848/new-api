# 交付总览（2026-11-05 通宵批次）

> 从 fork 到 P0/P1/P2 能力落地，共 **23 个 PR**（#16–#45）、**21 个 issue 关闭**，仅剩 **#3**（需要你拍板）。
> main：`4543e4d84`；所有 PR 走 devflow（S0–S8）+ CI 双检查后合并。

## 1. 交付清单

### 1.1 工程与底座
| PR | 内容 |
|---|---|
| #16 | AGPL 署名、CI（vet/build/test + 前端 typecheck/test）、自有镜像 `ghcr.io/wings1848/wings-api`、上游同步说明 |

### 1.2 额度（P0）
| PR | 内容 |
|---|---|
| #17 / #18 | 用户级无限额度（后端 + 用户列表/编辑 UI） |
| #19 | 5h/7d 双窗口数据层与记账（UTC+8 固定窗口、Redis 热计数、退款钳制、三库） |
| #20 | 请求前预检：429 + `X-RateLimit-*` + `Retry-After` + `X-Should-Retry:false`，观察模式 |
| #21 / #22 | 管理端/自助 API；用户编辑抽屉与钱包窗口卡片 |
| #23 | 80% 窗口预警（现有通知通道、Redis 去重） |

### 1.3 隐私（P0 + 精度）
| PR | 内容 |
|---|---|
| #24 | `pkg/privacy` 规则引擎（密钥/JWT/私钥/PII/银行卡、自定义规则、warn/block/redact、确定性占位符） |
| #25 | 入站脱敏中间件（改写 BodyStorage，覆盖所有计费路由；off/warn/redact/block 灰度） |
| #26 | 响应还原（JSON + SSE 跨 chunk，客户端看到真实值） |
| #27 | 日志与错误信息脱敏（SQL/ClickHouse 日志 + AuditLog + console 行） |
| #28 / #29 | dry-run 审计后端 + 管理端「隐私审计」页面 |
| #45 | 精度增强：协议根字段、续传字段排除、数字边界、IBAN、Suppression、fail-closed |

### 1.4 P1 商业化 + P2
| PR | 内容 |
|---|---|
| #35 / #37 | P1/P2 能力盘点、AstrLink/magpie 文件级移植清单与逐项去留、#3 决策记录 |
| #39 / #43 | 多协议 e2e：4 协议 × 流式/非流式/工具/图片/错误透传，共 **13/13**；mock 可重复执行 |
| #40 | 防滥用：在途并发上限（user/token，Redis 原子槽位）+ 异常检测（失败率/多 token 同 IP） |
| #41 / #42 | 报表：窗口维度 API + 管理端窗口报表弹窗 + 用户预计耗尽 |
| #44 | 渠道多账号：上游额度耗尽识别 → 换 key → 休息阶梯（30m→…→7d，懒恢复） |

## 2. 已关闭 issue（21）
#1、#2、#4~#15、#30~#34、#36、#38（详情见 GitHub；P0 全部、P1 全部、P2 可做项全部）

## 3. 运维开关（全部默认关闭/保守，按需开启）

| 变量 | 默认 | 作用 |
|---|---|---|
| `QUOTA_WINDOW_OBSERVE_ONLY` | true | 窗口预检只记录不拦截；关掉才真正 429 |
| `QUOTA_WINDOW_WARNING_PERCENT` | 80 | 窗口预警阈值 |
| `PRIVACY_INBOUND_MODE` | **warn** | off/warn/redact/block；默认只观测不拦截（用户确认） |
| `PRIVACY_POLICY_JSON` | 空 | JSON 策略覆盖（规则/白名单/Suppression） |
| `PRIVACY_INBOUND_FAIL_CLOSED` | false | 解析失败时 503 而非放行 |
| `PRIVACY_LOG_MODE` | off | off/warn/redact 日志脱敏 |
| `PRIVACY_PLACEHOLDER_KEY` | 随机 | 固定占位符密钥（多副本建议设置） |
| `CONCURRENCY_LIMIT_ENABLED` | false | 在途并发总开关 |
| `CONCURRENCY_LIMIT_PER_USER` / `_PER_TOKEN` | 0 | 并发上限（0=不限） |
| `CONCURRENCY_LIMIT_TTL_SECONDS` | 600 | 槽位 TTL 兜底 |
| `CONCURRENCY_LIMIT_FAIL_OPEN` | true | Redis 故障时放行；false=503 |
| `ABUSE_DETECT_ENABLED` | false | 异常检测开关 |
| `ABUSE_FAILURE_WINDOW_SECONDS` / `_MIN_REQUESTS` / `_RATIO` | 60 / 20 / 0.8 | 失败率规则 |
| `ABUSE_TOKEN_IP_MAX_TOKENS` | 5 | 同用户+IP 多 token 阈值 |
| `ABUSE_ALERT_COOLDOWN_SECONDS` | 300 | 告警冷却 |

> 灰度顺序建议：隐私默认已开 `warn` 观测；其余先开观察/告警类（窗口观察、异常检测），观察一周后再开拦截类（窗口拦截、隐私 redact、并发上限）。

## 4. 验证证据索引

| 证据 | 位置 |
|---|---|
| devflow 运行目录（每个 PR 的验收/红证/验证报告/复盘） | `runs/<run-id>/`（仓库内，本地未入库） |
| 多协议 e2e（13/13） | `e2e/multi-protocol/run.sh` + `mock_upstream.py` |
| 隐私引擎基准 | `pkg/privacy`；1KB 0.38ms / 10KB 3.7ms（N6000） |
| 三库矩阵 | quota window、管理 API、报表、隐私审计的 SQLite/MySQL 8.0/PostgreSQL 18 测试记录 |
| CI | GitHub Actions：Backend vet/build/test + Frontend typecheck/test（每次 PR 全绿） |

## 5. 已知限制 / 待办

1. **#3 已确认（2026-11-05）**：缓存 token 计入窗口；脱敏默认 `warn`；保持 AGPL 全开源。记录见 `decision-record-3.md`。
2. 隐私：本地模型 detector 不做；WebSocket 消息未做还原；未知路径回退旧扫描逻辑。
3. 并发/异常检测默认关闭；单 key 渠道额度耗尽仍走原自动禁用（未做休息）。
4. 报表：套餐维度需 join `user_subscriptions`（未做）；日志聚合在 Go 侧完成，超大日志量需后续优化。
5. e2e 手动项：真实厂商 key 回归（发布前跑一次）、4xx 参数错误细分、真实图片上传。
6. 渠道休息无「成功回调清零」，阶梯按 24h 衰减近似；懒恢复依赖下一次选 key。

## 6. 如何继续

- 上游同步：`README.zh_CN.md` 的「上游同步」章节（`git pull upstream main` → 冲突解决 → CI）。
- 每个新任务：`devflow init` → 按 S0–S8 推进；PR 保护要求 1 review，当前由 owner 用 `--admin` squash 合并。
- 后续优先：先按 #3 结论调整（如有），再考虑 P2 企业化（多租户/审计导出/一键交付/插件文档）与新需求。

---

*本文件由自动开发流程生成；所有数字来自 `gh pr list` / devflow runs / CI 记录，可复现。*
