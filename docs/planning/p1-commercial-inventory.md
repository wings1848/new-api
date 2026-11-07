# P1 商业化能力盘点与子任务拆解（issue #14）

> 状态：盘点完成，子任务已拆。代码路径基于 `main@e06d5a490`。
> 参考报告：`docs/research/2026-11-05-magpie-quota-window.md`（491 行）、`docs/research/2026-11-05-astrlink-privacy-port.md`（618 行），已入库。

## 1. 画布期望 → 现状 → 缺口

| # | 画布期望 | 现有实现（代码路径） | 缺口 | 子任务 |
|---|---|---|---|---|
| 1 | 多协议入口 OpenAI + Anthropic + Gemini | `/v1/chat/completions`、`/v1/completions`、`/v1/responses`、`/v1/messages`、`/v1beta/*`（`router/relay-router.go`），流式/工具/图片均已支持 | 缺系统化验收矩阵与接入文档 | #30 |
| 2 | 用量报表 / 费用估算 | usage-logs（`web/src/features/usage-logs/`）、dashboard、按天聚合（`model/usedata.go`） | 缺「5h/7d 窗口」「套餐/分组」维度与导出 | #31 |
| 3 | 防滥用：注册收口、并发限制、异常检测 | 注册开关/邮箱验证/Turnstile；模型 RPM/TPM 限流（`middleware/model-rate-limit.go`）；渠道自动禁用 | 缺**在途并发上限**与异常检测（失败率、多 token 轮换） | #32 |
| 4 | 渠道多账号 + 耗尽自动切换 | multi-key 渠道（`model/channel.go`）、重试（`service/relay_error.go`）、自动禁用 | 缺「上游账号额度耗尽」识别、换号休息阶梯、响应提交后不切换 | #33 |
| 5 | 参考项目可移植清单 | magpie/AstrLink 报告已在 `docs/research/`；#5/#6/#9~#13 已吸收主要设计 | 缺逐项去留结论与排期 | #34 |

## 2. 参考项目可移植清单（文件级 + 工作量）

### 2.1 magpie → wings-api（Go→Go，概念与算法级）

magpie 是 MIT 许可的桌面 AI 网关（`yetone/magpie`），代码量约 6 万行 Go。可移植部分集中在 3 个文件、约 800 行，核心算法 < 150 行。

| 文件 | 内容 | 状态 | 工作量 |
|---|---|---|---|
| `internal/limit/limit.go`（40 行核心） | 固定窗口纯函数：`anchor + floor((now-anchor)/N)*N`；多窗口全部通过才放行 | **已吸收**（#5 `service/quota_window.go`） | 0（已完成） |
| `internal/budget/budget.go` | 用量记录/窗口桶复用、reservation 估算（`body/4 + 平均 output`）、429 报告最晚重置窗口、cache token 口径开关 | 部分吸收：记账与「最晚重置」已做（#5/#6/#8）；**reservation 预估未做**（当前不预估，最多多放一个请求，见 #6 已知项） | 2–3 天（可选增强） |
| `internal/gateway/key_limit.go` | 预检中间件：429 + `Retry-After` + `X-Should-Retry:false`，在选渠道之前拦截 | **已吸收**（#6 `middleware/quota_window.go`） | 0（已完成） |
| `internal/gateway/routing.go`（rest ladder） | 休息阶梯 30m→2h→8h→24h→3d→7d、成功清零 | 待做（#33） | 2–3 天 |
| `internal/gateway/fallback.go` | 只在响应未提交时换上游 | 待做（#33；new-api retry loop 结构已具备） | 1 天 |
| `internal/gateway/rest_store.go` | 休息状态持久化（多副本一致） | 待做（#33；用 DB/Redis 替代 magpie 的单机 JSONL） | 1–2 天 |
| `internal/limit/limit.go` 锚点语义 | 改 cap 不动锚点，只有显式 reset 才动 | 可借鉴（#7 管理端改限额时保持窗口起点） | 已验证 |
| `X-Magpie-*` 头、单机 JSONL 账本、wails GUI、`internal/provider` 厂商差异化读数 | 平台/产品强绑定 | **不移植** | — |

### 2.2 AstrLink → wings-api（Apache-2.0 自有源码部分）

AstrLink 的隐私能力是「Apache-2.0 自有源码 + AGPL RelayKit 组合体」。**只可参考 `core/internal/privacy`、`jsonedit`、`systemprompt`、`contract/policy.go` 四块**（均属自有源码）；`relaykitbridge` 属于 AGPL 组合体，不抄。

| 文件/模块 | 内容 | 状态 | 工作量 |
|---|---|---|---|
| `core/internal/privacy/regex.go`（13 条内置正则 + validate/boundary 二次校验：Luhn、IBAN mod97、手机号去重、邮箱排除版本号） | 规则引擎 | **已独立实现**（#9 `pkg/privacy`：8 类内置规则 + Luhn/身份证校验位；未做 IBAN） | IBAN 可选 +1 天 |
| `core/internal/privacy/engine.go` + `types.go`（4 动作 allow/redact/block/warn、策略分支） | 三档策略 | **已独立实现**（#9：warn/block/redact；allow = 不命中） | 0（已完成） |
| `core/internal/privacy/document.go`（按协议根字段提取，跳过 base64/图片/音频/文件；工具声明默认不扫） | 入站范围界定 | **部分吸收**（#10 用 key 白名单 content/text/input/prompt/system/instructions + `data:` 跳过 + 工具定义不扫）；比 AstrLink 粗，可按协议精确根字段增强 | 2–3 天（增强） |
| `core/internal/privacy/continuation.go`（排除 `signature`、`encrypted_content`、`redacted_thinking.data` 等推理续传字段） | 防破坏续传 | **未做**：目前靠 key 白名单天然跳过这些字段，但若以后扫描范围扩大需要显式排除 | 1 天（随增强一起） |
| SSE 还原（`ShouldRestoreContentType` + `buffered.Replace` 幂等 + 禁 `Accept-Encoding` + 「还原→压缩」顺序） | 流式还原 | **已独立实现**（#11：ResponseWriter 包装、跨 chunk 拼接、Content-Length 删除）；`buffered.Replace` 幂等与 gzip 顺序需复核 | 1 天（复核） |
| 抑制规则 `suppressionFor`（误伤治理：版本号/日期/URL 等） | 降低误报 | 未做（#34 评估） | 2 天 |
| 失败语义 `InboundFailed` → 503 拒绝（fail-closed） | 安全兜底 | 未做：当前 #10 解析/重建失败 fail-open；#34 评估是否加 fail-closed 开关 | 0.5 天 |
| 本地模型 detector（Rust worker + ONNX Runtime + 37 MiB 模型） | 识别人名/地址 | **明确不做**（P2 再评估；多一个二进制 + C 依赖） | — |
| `core/internal/relaykitbridge/`（2224 行，AGPL 组合体） | 桥接层 | **不抄**（无许可收益，new-api 本身 AGPL） | — |

> 许可注意：若未来要双许可/闭源分发，只能搬 AstrLink 自有源码（Apache-2.0），保留 NOTICE/修改说明，不得使用其商标。

## 2.3 落实结论（issue #34）

逐项去留已定（不引入新依赖、不复制代码，仅借鉴设计）。

| 来源 | 项 | 结论 | 去向 / 理由 |
|---|---|---|---|
| magpie | `limit.go` 固定窗口算法 | 已完成 | #5 已实现（含 UTC+8 锚点、7d 周一） |
| magpie | `key_limit.go` 429 语义 | 已完成 | #6 已实现（`Retry-After`/`X-Should-Retry:false`/选渠道前拦截） |
| magpie | `budget.go` 记账 / 最晚重置 | 已完成 | #5/#6/#8 |
| magpie | `budget.go` reservation 预估 | **不做** | 当前「最多多放一个请求」（#6 已知项）可接受；出现刷额度再评估，不阻塞 P1 |
| magpie | `routing.go` 休息阶梯 | 待做 | 并入 **#33**（渠道额度耗尽换号） |
| magpie | `fallback.go` 只在响应未提交时切换 | 待做 | 并入 **#33** |
| magpie | `rest_store.go` 多副本休息状态 | 待做 | 并入 **#33**（用 DB/Redis，不用单机 JSONL） |
| magpie | pace 排序 / Codex 登录态轮询 | 不做 | 桌面 CLI 场景特有，与网关无关 |
| magpie | GUI / JSONL 账本 / 厂商差异化读数 / `X-Magpie-*` | 不移植 | 平台/产品绑定 |
| AstrLink | `regex.go` 规则与校验（Luhn 等） | 已完成 | #9 独立实现 8 类规则 + Luhn/身份证校验位 |
| AstrLink | `engine.go`/`types.go` 三档策略 | 已完成 | #9（warn/block/redact） |
| AstrLink | `document.go` 协议根字段提取 | 待做（P2） | 并入 **#36**（当前 key 白名单可用，精确化是增强） |
| AstrLink | `continuation.go` 续传字段排除 | 待做（P2） | 并入 **#36**（当前白名单天然跳过，扩大扫描范围前必须做） |
| AstrLink | 抑制规则（版本号/日期/URL） | 待做（P2） | 并入 **#36** |
| AstrLink | fail-closed（503） | 待做（P2） | 并入 **#36**（默认保持 fail-open + 告警） |
| AstrLink | SSE 还原细节 | 已完成 + 已复核 | #11 已实现；复核结论见下 |
| AstrLink | JSON worker 本地模型 | 不做 | 多二进制 + 37MiB 模型 + C 依赖，收益不成比例（P2 再议） |
| AstrLink | `relaykitbridge`（AGPL 组合体） | 不抄 | 无许可收益 |

### 复核结论（#11 相关，AstrLink 报告里的 3 个坑）

1. **响应压缩顺序**：new-api 的 `middleware/gzip.go` 只做**请求体解压**，relay 响应链没有响应压缩中间件；还原 writer 在 gin 写出前安装，不存在「先压缩后还原」问题。若未来加响应压缩，必须保证压缩在还原之后（外层）。
2. **幂等性**：占位符是 HMAC 确定性的、每次还原把占位符替换回原值；重复执行还原不会再命中占位符，天然幂等，不需要额外 `buffered.Replace`。
3. **`Accept-Encoding`**：网关不解压/重压上游响应，无需禁用；该坑只影响 AstrLink 自己的还原层。

### 许可与依赖

- 本仓库隐私实现为**独立实现**（#9~#13），未复制 AstrLink/magpie 代码，无需在文件头标注修改来源。
- 若未来直接搬运 AstrLink 自有源码四块，需附 Apache-2.0 全文、保留 NOTICE、标注修改，且不得使用其商标；`relaykitbridge` 永不搬。
- 两条主线均**未新增 Go 依赖**（`pkg/privacy` 纯 stdlib；窗口/审计用现有 GORM/Redis）。

## 3. 小规模开放测试前必须补的 3 件事

1. **注册收口**（已有开关，需确认默认与邀请码流程）
2. **全局/用户并发上限**（#32，避免 2 核机器被打满触发熔断）
3. **成本预警**：80% 窗口预警已做（#8）；再加「日成本超阈值告警」——并入 #32 或 #31

## 4. 已交付的 P0 能力（本轮盘点核对）

| 能力 | PR/位置 |
|---|---|
| 无限额度用户 | #4（`users.unlimited_quota`） |
| 5h/7d 双窗口数据层 | #5（`model/quota_window.go`、`service/quota_window.go`） |
| 预检 429 + 响应头 | #6（`middleware/quota_window.go`） |
| 管理端/自助 API | #7（`controller/quota_window.go`） |
| 80% 预警 | #8（`service/quota_window.go`） |
| 隐私规则引擎 | #9（`pkg/privacy`） |
| 入站脱敏 / 响应还原 / 日志脱敏 / dry-run 审计页 | #10/#11/#12/#13 |
| CI 与镜像 | #2（`.github/workflows/wings-ci.yml`、`wings-image.yml`） |

---

*盘点日期：2026-11-05；子任务：#30–#34。*
