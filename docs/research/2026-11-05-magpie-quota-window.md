---
title: magpie 配额窗口 / 额度耗尽自动换号调研
date: 2026-11-05
source: https://github.com/yetone/magpie
commit: 待核（原文 7061447 在 yetone/magpie 不存在）
target: wings-api (new-api fork)
original_path: .refs/magpie-report.md
note: 只读克隆 .refs/magpie 已删除；报告正文的 7061447 在该仓库查不到（疑似误拷 new-api 的 SHA），引用行号需重新 pin commit 才能复核
---

# magpie 配额窗口 / 额度耗尽自动换号 —— 代码级调研报告

> 调研对象：`https://github.com/yetone/magpie`，`git clone --depth 1`，HEAD 提交 `7061447` 附近的 master。
> 目的：判断哪些设计值得移植到 new-api（OpenAI 兼容网关），实现「每用户 5 小时 + 7 天双窗口 token 限额、内部人无限、额度耗尽自动切上游」。
>
> 所有结论标注 `文件:行号` 或函数名。标「未验证」的是推断或未跑到运行验证的部分。

---

## 0. 一句话结论

magpie 恰好**已经有**「一个 key 上挂多个窗口（5 小时 + 一周），每个窗口有 token 上限和/或成本上限，任一窗口超限就 429」的完整实现（issue #2227），
落地在 `internal/budget`（强制）+ `internal/access/limit.go`（窗口算法）+ `internal/gateway/key_limit.go`（429 入口），
是**同一门语言（Go）**、**同一类系统（本地网关）**，因此这份代码的概念与算法可以近乎原样照搬，只有存储层（单机 JSONL → Redis/Postgres）和「换号」那部分（桌面端重登 CLI）必须重写。

**最关键的三个判断：**

1. **窗口是固定周期锚定（fixed, anchored），不是滚动（rolling）。** `start = anchor + floor((now-anchor)/N)*N`，锚点存在 key 自己身上，改限额不移动锚点。见 `internal/access/limit.go:187-205`。
2. **没有定时重置任务。** `Window(now)` 是纯函数；窗口桶以 `start` 为键懒加载，旧桶自动丢弃。见 `internal/budget/budget.go:111-133`。
3. **检查在请求前，拒绝是 429 + `Retry-After` + 重置时间头；并发靠内存 reservation 兜，允许「大约超一个 call」的溢出。** 见 `internal/gateway/key_limit.go:64-81`、`internal/budget/budget.go:373-465`。

---

## 1. 技术栈与仓库结构

| 项 | 结论 | 证据 |
| --- | --- | --- |
| 语言 | **Go 1.26.8**（不是 Swift/TS） | `go.mod:3` |
| 规模 | 5069 个 `.go` 文件；`internal/` 77MB | `find` 统计 |
| 形态 | 桌面菜单栏应用 + 本地网关 + CLI + TUI + Web UI + Docker/Termux | `README.md` 开头；`go.mod` 依赖 `wailsapp/wails/v3`、`charmbracelet/bubbletea` |
| 网关地址 | `127.0.0.1:3425`，说 OpenAI Chat / OpenAI Responses / Anthropic Messages / Gemini 四种协议 | `README.md`「How it fits together」 |
| 自有数据存储 | **追加式 JSONL + JSON 配置，不是 SQLite** | `internal/usage/usage.go:277`、`internal/access/access.go:131` |
| SQLite 依赖用途 | 只为**读别人家 app 的库**（Codex `codex.db`、ZCode `db.sqlite`、Cursor、OpenCode 会话） | `internal/sessions/zcode.go:28`、`internal/sessions/codex_catalog.go:18-42` |
| 设计文档 | `docs/subsystems/*.md`，`gateway-routing.md` 564KB、`providers-accounts.md` 190KB | 目录 |

核心目录（与本题相关）：

```
internal/access/      网关 key 定义与 limit 数据模型（limit.go 222 行 = 窗口算法全部）
internal/budget/      每个 key 的预算强制（budget.go 488 行 = 全文最核心）
internal/gwbudget/    网关的「预算检查点」接口，fail-closed（gwbudget.go 124 行）
internal/gateway/     ~110k 行；key_limit.go=429 入口，fallback.go=换号/重试，routing.go=休息阶梯
internal/usage/       记录结构、计价、JSONL 账本与索引
internal/provider/    provider/账号/订阅窗口读数、routing 策略、pace
internal/davsync/     跨设备同步（WebDAV/S3/GitHub）
plugins/budget/       把 internal/budget 注册为插件（可关，关门即拒）
docs/reference.md     660-700 行是 key limit 的权威说明
```

---

## 2. 重要区分：magpie 有**两套**「配额」

搞混这两套会得出完全错误的移植结论。

### (A) 自己强制的「key 预算」——这才是要抄的

`internal/access/limit.go` + `internal/budget/`。

- 对象：**网关 key**（调用方给 agent 用的那个 key），不是上游账号。
- 语义：一个 key 最多 5 个 limit（`MaxLimits = 5`，`limit.go:39`），每个 limit 一个窗口，**全部都要有余量**才放行（`limit.go:41-44`、`budget.go:411-422`）。
- 文档原话：「$3 every 5 hours and $20 a week, as a subscription's two windows」`docs/reference.md:672-674`。

这与需求里「每用户 5 小时 + 7 天双窗口 token 限额 + 共享 key 的每日/每周上限」是**一一对应**的。

### (B) 只读的「上游订阅窗口」——只用于展示和排序，不强制

`internal/provider/subscription_usage.go:29`（`QuotaWindow`）、`internal/provider/claude_limits.go:26-38`（`five_hour` / `seven_day` 等 kind）、`internal/provider/planquota.go`

- 数据来源：厂商自己的接口（`/usage`）、响应头（Anthropic 的 `anthropic-ratelimit-unified-*`）、或 Claude Code 的 `rate_limit_event`。
- 用途：GUI 展示、Smart 路由的排序权重（pace）、判断某个账号「已耗尽」从而跳过。
- **不参与强制**，因为 magpie 无法阻止上游自己去花额度。

移植到 new-api 时，(A) 是「用户侧配额」，(B) 是「渠道侧余量观测」。两者要分别设计，不要合成一个字段。

---

## 3. 配额窗口的数据模型与算法

### 3.1 数据结构

```go
// internal/access/limit.go:21-36
type Limit struct {
    Period     string    // "day" | "week" | "month" | "days" | "hours"
    Days       int       // "days" 的周期，1..3650
    Hours      int       // "hours" 的周期，1..8760
    Tokens     int64     // token 上限，0 = 不限
    Cost       float64   // 估算成本上限（USD），0 = 不限
    CacheReads bool      // 是否把 cache read 计入 token
    Since      time.Time // 锚点：服务端自己写，客户端传的忽略
}
```

`Periods = []string{"day","week","month","days","hours"}`（`limit.go:92`）。

**5 小时窗口 = `{Period:"hours", Hours:5}`；7 天窗口 = `{Period:"days", Days:7}` 或 `{Period:"week"}`。**
两者语义不同，见下。

### 3.2 算法：固定周期锚定，非滚动

核心函数 `func (l *Limit) Window(now time.Time) (start, reset time.Time)`（`limit.go:187-205`）：

- 若 `cycle() > 0`（即 `days`/`hours`）：
  - 锚点 `anchor = l.Since`，未设置则用 Unix epoch（`limit.go:179`）。
  - **时钟被拨回到锚点之前**：返回 `[anchor, anchor+n)`（`limit.go:194-196`）。
  - 否则 **`start = anchor + (now-anchor)/n*n`**（`limit.go:197`）——即把时间轴按 n 等分，取当前所在格。
- 否则（日历窗口）：交给 `Window(period, now)`（`limit.go:209-222`）
  - `week`：本周一 00:00 至下周一 00:00（`back := (Weekday+6)%7`，`limit.go:214-216`）
  - `month`：本月 1 日 00:00 至下月 1 日 00:00
  - `day`：当日 00:00 至次日 00:00
  - **若 `Since` 落在本窗口内（= 做过 reset），起点改为 `Since`**（`limit.go:201-203`），终点不变。

> 所以周窗口是**固定日历周**（周一起），5 小时窗口是**固定 5 小时格**（锚定在你设置限额的那一刻），两者都不是 rolling window。
> 需求里说的「5 小时 + 7 天双窗口」，magpie 的做法是：5h 用 `hours`（从现在起每 5 小时一格），7d 用 `days/7` 或 `week`。

### 3.3 时区处理

- 日历窗口用 `now.Location()`（`limit.go:188, 211`），即**进程本地时区**。
- `days`/`hours` 周期**按绝对时间计**，注释明说「24h a day whatever the clocks do, so a restart or a change of time zone or summer time keeps it」`limit.go:182-185`。这是刻意的：订阅的 5 小时窗口不该因为夏令时变成 4 或 6 小时。
- 副作用：服务端如果 `TZ` 变了，日历窗口边界会跟着变，周期窗口不会。

### 3.4 重置语义

三种「重置」，代码里区分得很清楚：

| 动作 | 行为 | 证据 |
| --- | --- | --- |
| 自然到期 | 无需动作。`start` 变了就是新窗口，旧的用量自然不算 | 纯函数，无定时器 |
| 管理员改 cap | **不移动锚点**，已用量保留 | `sinceOf`：周期与 N 都没变 → `return prev.Since`（`limit.go:167-171`） |
| 显式 reset | 写 `Since = now`；`days/hours` 开新周期，日历窗口只丢弃「此刻之前的调用」、保留窗口终点 | `access.go:483-502`、`docs/reference.md:693-698` |

`sinceOf` 的完整规则（`limit.go:163-176`）：
- 周期和 N 都没变 → 保留旧 `Since`（改 cap 不重启窗口）。
- 周期或 N 变了，或从无限变有限 → `days/hours` 用 `now` 作新锚点；日历窗口清空 `Since`。
- 两个 limit 不允许落在同一窗口：`sameWindow`（`limit.go:87-89`）+ 校验（`access.go:474-481`）。

### 3.5 没有定时任务

`internal/budget/budget.go:111-133` 的 `sumsFor`：

- 每次检查时按当前 `start` 列表向 `state.sums` 取桶；
- 缺的桶用 `read(keyID, start)` 从 `usage.jsonl` 现算（`budget.go:135-147`）；
- **不在当前窗口列表里的旧桶直接丢弃**（`state.sums[k] = next`，`budget.go:131`）。

所以既没有 cron，也没有「重置瞬间」的一致性风险——窗口边界是算出来的，不是事件。

---

## 4. 计量口径

### 4.1 记录结构

`internal/usage/usage.go:30` 的 `Record`，关键字段：

```go
Input        int  // 未命中缓存的输入（不含 cache）
Output       int
CacheRead    int  // 从厂商缓存读到的 prompt token
CacheWrite   int  // 写入缓存的 token（Anthropic 的 cache_creation）
CacheWrite1h int  // 其中按 1 小时 TTL 写的（Anthropic 计费 2×，5 分钟的是 1.25×）
Reasoning    int  // 推理 token（已含在 Output 里计费）
```

注释明确：`CacheRead`/`CacheWrite` **不在 `Input` 里**（`usage.go:70-75`）。

### 4.2 从上游响应解析（最容易踩坑的地方）

`internal/gateway/chat.go:1044-1076` 的 `cUsage.usage()`：

- 先读 `prompt_tokens_details.cached_tokens` / `cache_write_tokens`（OpenRouter 风格）；
- 没有则退到 `prompt_cache_hit_tokens` / `cached_tokens` / `cache_read_input_tokens`；
- 若全为 0，再用 Anthropic 的 `cache_read_input_tokens` + `cache_creation_input_tokens`；
- **`whole` 判定**：OpenAI 的 `prompt_tokens` 是含缓存的，Anthropic 的 `input_tokens` 不含。
  `whole = u.PromptTokens >= CacheRead+CacheWrite`，只有 `whole` 为真时才做 `Input = prompt - cache_read - cache_write`（`chat.go:1054-1064`）。

> 这一条对 new-api 极有价值：**不同厂商的 `prompt_tokens` 语义不同，混着减会算出负数或重复计费。**

### 4.3 计入配额的口径

`internal/budget/budget.go:69-75`：

```
tokens = Input + Output + CacheWrite        // 默认
tokens += CacheRead                          // 仅当 Limit.CacheReads == true
```

即**默认不计缓存读**。成本则永远按全量算（`s.cost` 与 `CacheReads` 开关无关，`budget.go:57-66`）。
理由合理：缓存读便宜但量大，计入 token 会让 5 小时窗口瞬间烧完；而成本上限要反映真实花费。

文档 `docs/reference.md:681-684` 与 CLI 开关 **Count cache reads too** 对应这个字段。

### 4.4 计价

`internal/catalog/catalog.go:390-396`：

```go
func (p Price) CostSplit(input, output, cacheRead, cacheWrite, cacheWrite1h int) float64 {
    f := p.At(input + cacheRead + cacheWrite)   // 按总输入量选阶梯价
    return (input*f.Input + output*f.Output + cacheRead*f.CacheRead +
            (cacheWrite-cacheWrite1h)*f.CacheWrite + cacheWrite1h*f.OneHour()) / 1e6
}
```

- 价格单位 **USD / 百万 token**（`catalog.go:238`）。
- 阶梯：整通调用按 `input+cacheRead+cacheWrite` 落在哪一档定价。
- 未知价格：成本记 0 并计 `unpriced`（`budget.go:59-60`），不阻断。
- 这是**估算**，注释反复强调「不是厂商账单」（`limit.go:18-19`）。

### 4.5 流式请求怎么算

- 流式过程中每个事件到达就 `usage.add(ev.Usage)` 累积（`internal/gateway/relay_status.go:50-56`），`enc.finish()` 后才算完整（`relay_status.go:62`）。
- 结算时机：**流结束后**写一条 `Record`（`internal/budget/budget.go:24-27` 注释：「A streamed reply is settled when it ends, with the usage its vendor reported; one cut off before that counts what was recorded」）。
- 记录归属窗口：用 `rec.Time`（请求**开始**时刻），不是结束时刻（`budget.go` 包注释第 2 条；`budget.go:373-465` 的 reservation 也按 `start` 归桶）。
- 被拒绝的请求（`Rejected`）、探针（`IsProbe`）、自报（`IsReported`）、MCP 工具调用都**不计入**（`budget.go:102-104` 的 `counts`）。

---

## 5. 存储

| 数据 | 形态 | 路径 | 证据 |
| --- | --- | --- | --- |
| 调用记录（用量/成本的唯一真相） | 追加式 JSONL，无 schema | `~/.config/magpie/usage.jsonl` | `usage.go:276-277` |
| 读取加速 | 可丢弃的压缩块索引，512 行一块，只解重叠时间窗的块 | 内存 + 磁盘缓存 | `internal/usage/log_index.go:1-10, 27-31` |
| key + limit + `since` | JSON | `caller-keys.json`（`access.Path()`） | `access.go:131-172` |
| 上游休息状态（credits 阶梯） | JSON | `rests.json` | `internal/gateway/rest_store.go:21-26` |
| 配额历史 | 每设备一份，可合并 | `magpie/usage/<id>.magpie-quotas` | `internal/davsync/usage.go:1-13` |

**跨设备**：`internal/davsync`（WebDAV / S3 / GitHub）。`caller-keys.json` 属于 settings 部分会被同步（`davsync.go:995-1001`）；用量按天分片单独共享（`davsync/usage.go:3-7`）。
`budget` 的注释也承认它读文件是为了「a gateway served by another process too」（`budget.go:235-237`）。

**跨进程/多实例的坑（重要）**：强制计数在**进程内存** `var state`（`budget.go:90-94`），用互斥锁保护（`budget.go:400-402`）。
两个进程共用一个 `usage.jsonl` 时，各自维护自己的 sums，**会各放行到上限 → 实际超发一倍**。
new-api 如果多副本部署，这一点必须换成 Redis 原子计数，不能照抄。

---

## 6. 检查时机与拒绝语义

### 6.1 请求前预检（middleware）

`internal/gateway/key_limit.go:36` 的 `keyLimited`：

- 跳过条件（`key_limit.go:43`）：非 POST、`/_magpie/*`、`/mcp/*`、`*/count_tokens`、`/v1/magpie/keys`。
- 需要算成本上限或 body 大小未知或 gzip 时，先把 body 读进来算大小与 model（`key_limit.go:49-63`）。
- `reserveFor`（`key_limit.go:91-104`）→ `gwbudget.Limiting` → `budget.Reserve`。

### 6.2 拒绝返回什么

`key_limit.go:66-80`：

| 情况 | 状态码 | 头 | errType |
| --- | --- | --- | --- |
| 有限额但插件没开（fail-closed） | **503** | `X-Should-Retry: false` | `gateway_key_limit_off` |
| 超限 | **429** | `Retry-After: <秒>`、`X-Magpie-Limit-Reset: <RFC3339>`、`X-Should-Retry: false` | `gateway_key_limit` |

错误文案（`budget.go:313-330`）形如：

> `magpie gateway key "Family" has used 200001 of its 200000 input, output and cache-write tokens for this 5-hour cycle; it resets at 2026-11-05 12:00 CST`

`X-Should-Retry: false` 的注释解释了原因：**Anthropic / OpenAI SDK 会自动重试 429 和 503**，不显式关掉会形成重试风暴（`key_limit.go:74-75`）。
`X-Magpie-Limit-Reset` 让另一个 magpie 实例把上游的 key 也歇到同一时刻（`key_limit.go:25-28`）。

### 6.3 并发：reservation

`internal/budget/budget.go:373-465` `reserve`：

- 估算：`est = body/4`（4 字节≈1 token）+ **该窗口内每调用平均 output**（`budget.go:436-441`）。
- 判定：`tokensUsed + heldTokens >= limit.Tokens` 即拒绝（`budget.go:411-421`）。
- 多窗口时：**每个窗口各记一份 reservation**，但同一窗口的多个 limit 只记一次（`budget.go:427-460`）。
- 释放：`defer release()`（`key_limit.go:82`），`sync.Once` 保证幂等（`budget.go:461-463`）。

注释坦白代价：「parallel requests overshoot by about one call」（`budget.go:20-21`）。

### 6.4 拒绝时报告哪个窗口

多窗口全部要过；若都不够，报告**重置时间最晚的那个**（`budget.go:411-421`、`limit.go` 之外的 `Binding` 同理 `budget.go:271-292`）。
理由：用户在被拒到那一刻之前都过不了，说最早重置的那个会误导。

---

## 7. 账号轮换与失败重试

### 7.1 候选集

`internal/gateway/fallback.go:1-15` 包注释：

> 「A provider with several keys on is several candidates, one per key, in order, and so is a subscription with several accounts on: **when one account runs out, the next account of the same provider takes the request before any fallback model does.**」

- 候选生成：`perKey` / `perKeyOf` / `accountsOn` / `planGroup`（`fallback.go:256, 344, 661`）。
- 排序策略（`internal/provider/routing.go:21-62`）：`order`、`rotate`、`usage`、`pace`、`weight`、`free`、`fast`、`cheap`、`drain`、`expiring`，默认 `smart`。
- **`smart` 的核心指标是 pace**：`func (a Allowance) Pace(model string, now time.Time)`（`routing.go:656`）——
  「the share of its week an account has left for model, per hour until that week renews」，即**剩余额度 ÷ 距重置的小时数**，越大越优先。
  `budgetSpan = 24h`（`routing.go:616`）：**5 小时窗口不算 budget，只有 ≥1 天的窗口才算**——因为 5 小时窗口重置后剩下的本来就浪费了。

### 7.2 换号的两种触发

1. **本地读数已满**：`capHold`（`fallback.go:105-141`）、`spendHeld`（`fallback.go:128`）。读到窗口 used ≥ cap 就把账号标 capped 跳过。
2. **上游拒绝**：402/429/额度类错误 → 记 rest，本次请求换下一个候选。

### 7.3 只在「还没写出任何字节」时换

`fallback.go:5-7`：

> 「The request goes to the next one **only while none of the reply has been sent**, so an agent sees one clean answer from whoever gave it, never a half from each.」

这条对 new-api 很关键：换渠道只能发生在**响应提交前**。

### 7.4 休息（rest）阶梯

`internal/gateway/routing.go:242-296`：

| 原因 | 时长 | 常量 |
| --- | --- | --- |
| 余额/额度耗尽（可恢复） | 30 分钟起，逐级 2h → 8h → 24h → 3d → 7d | `creditRest`（`routing.go:237`）、`creditLadder`（`routing.go:296`） |
| 上游说额度没了但没说何时恢复 | 15 分钟 | `quotaRest`（`routing.go:238`） |
| 普通失败 | 1 分钟 | `fallbackCooldown`（`fallback.go:43`） |
| 厂商限流 | 30s~30min，按厂商 `Retry-After` | `longestWait`（`routing.go:239`）、`longestRateRest`（`routing.go:260`） |
| 账号被限制（403） | 2 小时 | `restrictedRest`（`routing.go:279`） |
| 厂商说重置时间 | 用它，上限 8 天 | `longestQuota`（`routing.go:249`） |

- 阶梯只对 credit 类错误（`creditBackoff`，`routing.go:320-336`）：**同一个 key 反复被拒会越歇越久**，成功后清零（`clearRest`，`routing.go:224-231`）。
- 只有 ladder 的 rest 落盘 `rests.json`（`rest_store.go:10-18`），其它重启后重新探测。

### 7.5 全部耗尽时说什么

`fallback.go:980-1015` 的 `cappedError`：列出每个账号的原因与恢复时间，告诉用户最快何时可用。
这是很好的 UX 参考：**不要只返回一个干巴巴的 429，要说明「谁、为什么、什么时候回来」。**

### 7.6 桌面端特有的「重登下一个账号」

`internal/provider/codex_switch.go:593` 的 `KeepOnAnAccountWithRoom`：每 5 分钟看 CLI 当前登录的账号，额度耗尽就**改写 CLI 的登录态**到下一个账号。
这是为了绕过 Codex/Claude Code 自己读账号额度后灰掉发送按钮的问题（`codex_switch.go:1-30` 有完整解释）。
**这一部分对 new-api 完全无意义**（new-api 没有本机 CLI 登录态），但里面的「近满阈值 90%」`codexNearShare`（`codex_switch.go:66`）和「近满时 1 分钟一次、否则 5 分钟」的轮询策略（`codexLookEvery`，`codex_switch.go:71-90`）值得借鉴。

---

## 8. UI / API 暴露了什么

| 出口 | 内容 | 证据 |
| --- | --- | --- |
| `GET /v1/magpie/limit` | `{limited, key, heldTo[], models[], limits[], limit, issuerLimits[]}` | `key_limit.go:119-177` |
| 每个 `Status` | `period, days, hours, start, reset, calls, tokens, cost, unpriced, inFlight, reserved, reservedCost, tokenLimit, costLimit, cacheReads, tokensLeft, costLeft, spent` | `budget.go:193-216` |
| `GET /v1/magpie/quotas` | 上游订阅/套餐的窗口（5 小时、周、月）与余额 | `internal/usage/window_holds.go:21-23`、`internal/cli/quota_cli.go:25-31` |
| `GET /v1/magpie/usage` | 用量汇总（与 Usage 页同源） | `internal/usageapi/usageapi.go:3, 67` |
| CLI | `magpie gateway-key limit <id> list/5h --tokens 200k/add week --cost 20/reset/off` | `docs/reference.md:645-652` |

`limit` 与 `limits` 并存是刻意的兼容设计：老版本 magpie 只读 `limit`（`key_limit.go:121-123`）。

---

## 9. 可直接借鉴（概念级） vs 不能直接移植

### 9.1 可以直接借鉴（Go→Go，概念与算法级）

1. **窗口 = 纯函数 + 锚点**。`limit.go:187-222` 整个函数 40 行，无状态、无定时器，可原样移植。
2. **一个主体挂多个窗口，全部通过才放行**；拒绝时报「最晚重置」的那个。`limit.go:41-44` + `budget.go:411-421`。
3. **改 cap 不动锚点，只有显式 reset 才动锚点**。`sinceOf`，`limit.go:163-176`。
4. **计数从日志重算，进程重启不丢**；窗口桶按 `start` 复用与丢弃。`budget.go:111-147`。
5. **预检 + 内存 reservation**，估算式（`body/4 + 平均 output`），接受「大约超一个 call」。`budget.go:427-465`。
6. **429 + `Retry-After` + 重置时间头 + `X-Should-Retry: false`**。`key_limit.go:66-76`。
7. **fail-closed 的限额检查点**：限额插件没加载就 503 拒绝，绝不默认放行。`gwbudget.go:1-9, 105-124`。
8. **只在响应未提交时换上游**。`fallback.go:5-7`。
9. **休息阶梯 + 成功清零 + 落盘**。`routing.go:296, 320-336`；`rest_store.go`。
10. **pace 排序（剩余额度 / 距重置小时数）**，且 5 小时窗口不算 budget。`routing.go:616-700`。
11. **「没有 key 或 key 无限额 = 不限」**天然实现了「自己人无限额度」。`budget.go:13-14`、`issue.go:373-375`。
12. **缓存 token 的口径开关**（默认不计 cache read，成本照算）。`budget.go:69-75`。

### 9.2 不能直接移植（语言/平台/架构依赖）

| 部分 | 为什么不能抄 |
| --- | --- |
| `internal/provider/codex_switch.go` 重登 CLI | 改的是本机 Codex/Claude Code 的登录文件与 Keychain，纯桌面行为 |
| `internal/agent/`（118 个 agent 的配置改写） | magpie 的产品核心，与网关配额无关 |
| `wails` GUI / 菜单栏 / bubbletea TUI | 桌面壳 |
| **单机 JSONL 账本** | 无并发写锁、无跨实例一致性；new-api 多副本会超发（见 §5） |
| `internal/sessions/` 读别家 SQLite | 逆向别的 app 的库，与配额无关 |
| `X-Magpie-*` 头 | 自家实例间协议 |
| `modernc.org/sqlite` + Go 1.26 | new-api 用 GORM + MySQL/PG/Redis |
| `provider`/`presets`（几十家厂商的差异化读数） | 每家的响应头与字段都不同，且是 magpie 的业务资产 |

**一句话**：抄 `limit.go` + `budget.go` + `key_limit.go` 三份文件（合计约 800 行，其中算法核心不到 150 行），别抄周边。

---

## 10. 对 new-api 落地 5h/7d 双窗口的 5 条具体建议

> 已核对 new-api 现状（<https://github.com/QuantumNous/new-api> @ `706144769e5b`）：
> `Token{RemainQuota, UnlimitedQuota, UsedQuota}`（`model/token.go:14-37`）、
> `User{Quota, UsedQuota}`（`model/user.go:97-98`）、
> Redis 原子预扣 Lua（`model/quota_reserve.go:19-67`，入口 `TryReserveUserQuota:164` / `TryReserveTokenQuota:202`）、
> 按天聚合的 `QuotaData{CacheReadTokens, CacheCreationTokens}`（`model/usedata.go:10-28`）、
> 请求前扣费 `PreConsumeTokenQuota`（`service/quota.go:406`）、
> 换渠道重试 `DecideRelayRetry`（`service/relay_error.go:20`）。

### 建议 1：新增「窗口用量」表，别往 token 行上加字段

```
token_window_usage(
  token_id, window_kind('5h'|'7d'), window_start,
  tokens_used, cost_used, reserved_tokens, reserved_cost,
  PRIMARY KEY (token_id, window_kind, window_start)
)
```

- `window_start` 用 magpie 的算法算：`anchor + floor((now-anchor)/N)*N`，锚点存 `tokens.window_anchor`（照 `limit.go:197`）。
- 一个请求命中哪些窗口就写哪几行；查询时只查 `window_start = 当前窗口`。
- **不要**用「按天/按小时 rollup 再求和」来实现 5h 窗口——5 小时窗口和自然小时不对齐，rollup 一定算错。

边界处理：
- **跨窗口边界**：用量记在**请求开始时刻**所属的窗口（`budget.go` 包注释第 2 条、`Append` 用 `rec.Time`），不要用完成时刻。否则一个跑了 40 分钟的流式请求会被算进下一个 5 小时窗口，用户会看到「刚重置就又满了」。
- **窗口重置瞬间**：不要写重置任务。查询时算 `window_start` 即可；新窗口没有行 = 用量 0。若用 Redis，给窗口 key 设 `TTL = 2×窗口长度`，让旧 key 自动消失（magpie 是靠「丢弃不在当前列表的桶」，`budget.go:131`）。
- **并发**：把 `model/quota_reserve.go:19-67` 的 Lua 从「余额够就扣」扩展成「**每个窗口都够才扣，且每个窗口各扣一份 reservation**」，脚本里对每个窗口 key 做 `HINCRBY`；任何一个不够就整体回滚（Lua 天然原子）。reservation 估算用 `body/4 + 该 token 近窗口平均 output`（照 `budget.go:436-441`）。

### 建议 2：结算口径对齐，尤其 cache 与流式

- token 口径：`tokens = (prompt_tokens - cache_read - cache_write) + completion_tokens + cache_write`，**默认不计 cache_read**，另给一个 `count_cache_reads` 开关（`budget.go:69-75`）。
- 必须先判断厂商的 `prompt_tokens` 是否含缓存（`chat.go:1054-1064` 的 `whole` 判定），否则 DeepSeek/Anthropic 与 OpenAI 混用会算出负数。
- 成本口径照 `catalog.go:390-396`：分 input / output / cache_read / cache_write(5m) / cache_write(1h) 五项，按总输入量选阶梯，单位 USD/1e6。
- 流式：**最后一个 usage chunk 到达时结算**；连接中断按已累计部分结算并打「部分」标记（magpie: `relay_status.go:50-62`、`budget.go:24-27`）。
- new-api 已有 `QuotaData.CacheReadTokens/CacheCreationTokens`（`model/usedata.go:26-27`），可直接复用为窗口表的输入，不用重写解析。

### 建议 3：429 语义与 SDK 兼容

- 状态码 **429**（不是 403）。new-api 现在预扣失败走 `StatusForbidden`（`service/billing_session.go:222-223`），语义上是「余额不足」，与「窗口超限」不同，建议分开。
- 必带头：`Retry-After: <秒>`（= 最晚重置窗口的剩余秒，`budget.go:332-334`）、`X-Should-Retry: false`。
  **这一条不是可选的**：OpenAI/Anthropic 官方 SDK 对 429 默认重试，没有这个头会出现「额度已尽 → SDK 自动重试 3 次 → 每次都被拒」的放大效应（`key_limit.go:74-75` 明确写了这个理由）。
- 错误体里要给出：哪个窗口、用了多少、上限多少、何时重置（`budget.go:313-330`）。
- 「自己人无限」在最前面短路：`Token.UnlimitedQuota == true` 或用户属于白名单 group 时直接跳过全部窗口检查（magpie 的等价物是 `who.KeyID == "" || len(limits) == 0`，`budget.go:375-377`）。

### 建议 4：把「用户额度耗尽」和「渠道失败」彻底分开

这是最容易做错的地方。new-api 的 `DecideRelayRetry`（`service/relay_error.go:20`）会根据错误决定是否换渠道重试。
如果 429 窗口超限被当成「上游错误」，会：**把该用户的重试额度耗在换渠道上，试遍所有渠道后仍然失败，还会触发渠道自动禁用**（`AutomaticDisableChannelEnabled` 的误伤）。

- 窗口超限的错误码要在 `DecideRelayRetry` 里**显式判定为不可重试**（对标 magpie 把 `gateway_key_limit` 在网关最外层就拦掉，根本不进 provider 选择——`key_limit.go:36` 在 `serve` 之前）。
- 反过来，渠道侧「上游账号额度耗尽」才该换渠道，且只在**响应未提交**时换（`fallback.go:5-7`）；new-api 的 retry loop 已经是这个结构，只需保证 `IsStream` 已写出后不换。
- 换渠道时的休息：照 `routing.go:296` 的阶梯（30m → 2h → 8h → 24h → 3d → 7d），别用固定 1 分钟——否则一个欠费的 key 会被每 30 分钟打一次（`routing.go:288-295` 的注释就是这个真实故障）。

### 建议 5：管理员改限额 / 重置的两个安全规则

- **改 cap 不重置窗口**：只更新 cap 字段，不动 `window_anchor`（`sinceOf`，`limit.go:167-171`）。否则管理员把 5h 从 200k 调到 300k，用户已用 190k 会瞬间归零，相当于白送一轮。
- **改周期长度（5h→6h）或换窗口类型，必须重置锚点**：否则当前窗口的边界会变成无法解释的半截（`sinceOf` 的 else 分支，`limit.go:172-175`）。
- **显式 reset**：写 `anchor = now`。对 `days/hours` 是开新周期；对日历周/月窗口是「丢弃此刻之前的调用，保留窗口终点」（`access.go:483-502`、`docs/reference.md:693-698`）——后者很重要，不然用户一 reset 周窗口就从周三变成周三到周三，永久漂移。
- 改限额/重置要**审计留痕**：new-api 有 `model/audit_log.go`，把 old/new cap、old/new anchor 写进去。

---

## 11. 未验证 / 存疑

- 「`usage.jsonl` 在多进程同时追加是否安全」：`Append` 用 `O_APPEND`（`usage.go:351-353`），单条 < PIPE_BUF 时 POSIX 语义大致安全，但**未实测**。
- 5 小时窗口的「用户感知重置时刻」是否与上游订阅的真实窗口对齐：magpie 中两者是**独立**的（本地 5h 锚点是设置时刻，上游 5h 是厂商决定），未验证是否有意为之。
- `docs/subsystems/gateway-routing.md` 中部分 issue 编号被替换成 `n`（如 `(#n, budget.Reserve)`），疑为文档生成时的脱敏，未追查原因；本文引用以代码为准。
- 未运行 magpie 的测试或二进制，所有结论来自静态阅读；`go test` 未执行。

---

## 12. 证据索引（快速定位）

| 主题 | 位置 |
| --- | --- |
| 窗口算法（核心 40 行） | `internal/access/limit.go:187-222` |
| 锚点保留规则 | `internal/access/limit.go:163-176`（`sinceOf`） |
| 多窗口定义与上限 | `internal/access/limit.go:21-60`、`:39`（`MaxLimits=5`） |
| 强制逻辑 / reservation | `internal/budget/budget.go:373-465` |
| 多窗口全通过 + 报最晚重置 | `internal/budget/budget.go:411-421` |
| 拒绝文案 | `internal/budget/budget.go:313-334` |
| token 口径 | `internal/budget/budget.go:69-75`、包注释 `:1-28` |
| 429 / 503 与头 | `internal/gateway/key_limit.go:64-81` |
| fail-closed 检查点 | `internal/gwbudget/gwbudget.go:1-9, 105-124` |
| 记录结构 | `internal/usage/usage.go:30-130` |
| 缓存口径解析 | `internal/gateway/chat.go:1044-1076` |
| 计价 | `internal/catalog/catalog.go:390-396` |
| 存储路径 | `internal/usage/usage.go:276-277` |
| 日志索引 | `internal/usage/log_index.go:1-31` |
| 换号候选与失败 | `internal/gateway/fallback.go:1-15, 105-141, 980-1015` |
| 休息阶梯 | `internal/gateway/routing.go:236-296`、`rest_store.go:10-26` |
| pace 排序 | `internal/provider/routing.go:616-700` |
| 上游窗口 kind | `internal/provider/claude_limits.go:26-38` |
| API 出口 | `internal/gateway/key_limit.go:119-177` |
| 权威文档 | `docs/reference.md:660-700` |
