---
title: AstrLink 隐私检测/脱敏能力 → new-api 移植调研
date: 2026-11-05
source: https://github.com/Calcium-Ion/AstrLink
commit: 19fe789c860f5dee17a67a4a47a5d0b9ddb758bf
target: wings-api (new-api fork)
original_path: .refs/astrlink-report.md
note: 调研时的只读克隆 .refs/astrlink 已删除；按 commit 可重拉
---

# AstrLink 隐私检测/脱敏能力 → new-api 移植调研报告

> 调研对象：<https://github.com/Calcium-Ion/AstrLink> @ `19fe789c860f`（调研时 `git clone --depth 1`）
> 目标项目：<https://github.com/QuantumNous/new-api> @ `706144769e5b`（现用 `wings-api` 的 `upstream` remote 对照）
> 性质：只读代码调研，未修改任何仓库代码。行号以本次 clone 的版本为准。
> 机器：本机 amd64（Intel N6000），Go 1.27.3。

---

## 0. 结论先行

1. **可抄的只有 `core/internal/privacy` 这一个包**（5069 行非测试代码，794 个测试全绿）。
   它是纯 Go、无 cgo、只依赖 `gjson/sjson` 和 4 个内部包，是整个项目里最干净的一块。
2. **不能整包理解的三个前提**（任务描述与仓库实际有偏差，见 §1）：
   - 不是纯 Go 项目：Go 只管网关，本地模型跑在 **Rust sidecar + ONNX Runtime** 里。
   - 默认形态是 **Tauri 桌面应用**，但确实有 `astrlink-core serve` 服务端形态（Dockerfile 打的就是它）。
   - 许可不是"Apache-2.0 随便抄"：AstrLink 自有源码是 Apache-2.0，但 `core` 链接了 **new-api 的 RelayKit（AGPL-3.0）**，`LICENSING.md` 明确说组合后整体按 AGPL 分发。
3. **真正难搬的不是检测规则，是"还原"**：SSE 半截占位符、跨轮次占位符稳定、IPv4 池冲突、上游 gzip。
   这部分和 HTTP 写出层强耦合（`ingress/restore_writer.go` + `protocol_restore.go` 共 1632 行），在 gin 下要重写而不是复制。
4. **最大的架构缺口：AstrLink 的策略是单例**（`GetPolicy(ctx, DefaultPrivacyPolicyID)`，`policy_store.go:22-28`，`Scope` 参数被丢弃）。new-api 是多租户网关，移植必须补一层 policy 作用域（user / token / group / channel）。

---

## 1. 与任务前提的偏差（重要）

| 前提 | 实际 | 证据 |
| --- | --- | --- |
| "Go 语言项目" | Go 后端 + TS 前端 + **2 个 Rust crate** + Swift + Ruby | `find . -name '*.go' \| wc -l` = 833；`apps/privacy-worker/Cargo.toml`、`apps/classifier-worker/Cargo.toml`、`apps/desktop/src-tauri/Cargo.toml` |
| "Apache-2.0" | 自有源码 Apache-2.0，**但 core 链接 RelayKit（AGPL-3.0），组合体按 AGPL 分发** | `LICENSE` 第 1 行；`LICENSING.md`（中文 §"自有源码与 RelayKit"）；`core/go.mod:6` `github.com/QuantumNous/new-api/relaykit v0.3.5` |
| "桌面程序还是 HTTP 服务" | **两者都有**：默认 Tauri 桌面（sidecar 模式），另有 `serve` 子命令做独立服务 | `core/cmd/astrlink-core/main.go:31-33`；`core/cmd/astrlink-core/serve.go:36-43`；`Dockerfile` 全文 |
| "移植到 new-api" | AstrLink 的 core **本来就依赖 new-api 的 relaykit**，是同族项目 | 同上 `core/go.mod:6` |

补充：`new-api` 自身是 **AGPL-3.0**（`.refs/new-api/LICENSE:1-2`）。Apache-2.0 代码并入 AGPL 项目是许可兼容的（单向），但必须保留署名/NOTICE/变更说明（见 §9）。

---

## 2. 进程形态与入口

### 2.1 两种跑法

**A. 桌面（默认）**：Tauri 外壳拉起 Go sidecar。

- 入口：`core/cmd/astrlink-core/main.go:32`（`func main()`），无子命令时进入桌面模式。
- 关键 flag（`main.go:61-76`）：`-control-listen`、`-inference-listen`、`-parent-pid`、`-control-token-stdin`、`-kek-stdin`/`-kek-file`、`-privacy-worker`、`-max-concurrent-inspections`、`-max-request-body-mib`。
- 桌面耦合点：控制端口用 per-start token（`-control-token-stdin`），父进程退出即自查（`-parent-pid`），本地 KEK 从 stdin 或 keyring 读（`core/internal/localkey`、`apps/desktop/src-tauri/src/kek_store.rs`）。

**B. 服务端**：`astrlink-core serve`

- `core/cmd/astrlink-core/main.go:31-32` → `runServeCommand`（`serve.go:90`）。
- 纯环境变量配置（`serve.go:36-43`）：`ASTRLINK_DATA_DIR`（默认 `/data`）、`ASTRLINK_LISTEN`（默认 `0.0.0.0:8317`）、`ASTRLINK_OUTBOUND_PROXY`、KEK 文件、控制台重置密码。
- `Dockerfile` 打的就是这个版本（`EXPOSE 8317`，`CMD ["serve"]`，`Dockerfile:114-129`），内嵌 Web 控制台（`-tags webui`，`Dockerfile:41-47`）。
- 结论：**敏感信息检测这条链路本身不依赖桌面**，服务端形态能跑；依赖桌面的是策略编辑 UI、warn 通知、raw 审批。

### 2.2 HTTP 入口

不是 HTTP 代理（没有 `http.Proxy` / CONNECT），是**网关自身的 ingress handler**：

- `core/internal/ingress/handler.go:1476` 行，`Handler` 结构体在 `handler.go:118-135`。
- 请求体在内存里缓冲后交给检测（`handler.go:794-834`）。
- 响应侧用包装 writer 还原（`restore_writer.go`）。

---

## 3. 敏感信息检测

### 3.1 两种 detector

`core/internal/privacy/types.go:12-24`：

```go
ModeRegex      Mode = "regex"
ModeLocalModel Mode = "local_model"
```

选择逻辑在 `engine.go:109-121`：`local_model` 用注入的 model detector；`regex` + `RegexSourceCustom` 用自定义规则编译；否则用内置 `NewRegexDetector()`。

### 3.2 敏感类型（10 类）

`core/internal/privacy/types.go:109-120`：

| Kind | 常量 | Regex 能出？ | 默认开启？ |
| --- | --- | --- | --- |
| 通用密钥 | `common_secret` | ✅ | **✅ 唯一默认开启** |
| 银行卡 | `payment_card` | ✅ | ❌ |
| 账号/IBAN | `account` | ✅ | ❌ |
| 邮箱 | `email` | ✅ | ❌ |
| 电话 | `phone` | ✅ | ❌ |
| URL | `url` | ✅ | ❌ |
| IP | `ip_address` | ✅ | ❌ |
| 人名 | `private_person` | ❌（仅模型） | ❌ |
| 地址 | `private_address` | ❌（仅模型） | ❌ |
| 日期 | `private_date` | ❌（仅模型） | ❌ |

- 规范顺序：`contract/policy.go:110-121`（`PrivacyKinds`）。
- Regex 可出的 7 类：`contract/policy.go:292-301`（`RegexDetectorKinds`）。
- 默认规则：`contract/policy.go:168-179`（`DefaultPrivacyKindRules`，只有 `common_secret` 是 `Enabled: true`）。
  注释写明了理由：邮箱/电话/地址在 coding agent 的 prompt 里全是测试数据和文档，默认开全会误伤。

### 3.3 内置正则（13 条，不是"一大本规则库"）

`core/internal/privacy/regex.go:86-176` `builtinRegexDefinitions()`：

| 行 | Kind | 说明 |
| --- | --- | --- |
| 90 | common_secret | `sk-*` / `AKIA*` / `AIza*` / `ghp_*` / `github_pat_*` |
| 100 | common_secret | `astr_` + 43 base64url（自家 token） |
| 109 | common_secret | `Bearer <token>`，只替换 token 部分（`(?P<value>...)`） |
| 122 | common_secret | `api_key= / token: / secret= / password:` 赋值，只替换值 |
| 128 | payment_card | 13–19 位数字 + Luhn + 分组形状校验 |
| 136 | email | 排除 `pkg@1.2.3` 这类版本号 |
| 141 | account | IBAN（mod 97 校验） |
| 146 | account | `account number: 1234...` 上下文式 |
| 151 | phone | `+国家码` 形式 |
| 156 | phone | `(123) 456-7890` 形式 |
| 161 | url | `https?://...` |
| 166 | ip_address | IPv4 |
| 171 | ip_address | IPv6（保守：必须含数字或 ≥4 个冒号） |

关键设计：**每条规则可带 `validate` 和 `boundary` 二次校验**（`regex.go:22-73`），不是纯正则命中就报。
例：`payment_card` 用 Luhn + 分组布局（`regex.go:318-370`）；`phone` 要求 10–15 位且非全同数字（`regex.go:397-400`）；`email` 排除版本号尾缀（`regex.go:384-395`）。

### 3.4 自定义规则

- 类型：`contract/policy.go:300-320` `PolicyRegexRule{Kind, Pattern}`。
- 上限：64 条 / 单条 512 rune（`contract/policy.go:48-51`）。
- 编译：`regex.go:210-226` `NewCustomRegexDetector`，Go `regexp`（RE2，无回溯）。
- **值分组约定**：`(?P<value>...)`，只替换该组，其余保留可读（`regex.go:20`，`regex.go:54-73`）。

### 3.5 本地模型（可选，默认不启用）

| 项 | 值 | 证据 |
| --- | --- | --- |
| 默认模型 | AstrLink Guard（中英，专为 prompt/code 调） | `core/internal/privacymodel/astrlink_guard.go:85-96` |
| Repo | `QuantumNous/astrlink-guard` | `astrlink_guard.go:14` |
| INT8 体积 | **38,971,636 B ≈ 37.2 MiB** | `astrlink_guard.go:101-107` |
| INT8 估算内存 | 256 MiB | 同上 |
| FP32 体积 | 149,179,699 B ≈ 142 MiB | `astrlink_guard.go:108-112` |
| 推理框架 | **ONNX Runtime 1.23.2**（动态库 `libonnxruntime.so`） | `Dockerfile:60-70` |
| Go↔模型边界 | **独立进程**，Rust `astrlink-privacy-worker`，长度前缀 JSON 帧 | `core/internal/privacyworker/client.go:1-8`、`client.go:19-52` |
| Worker 规模 | Rust 6854 行（`apps/privacy-worker/src/*.rs`） | `wc -l` |
| Worker 依赖 | `ort`（ONNX）、`tokenizers` + `onig`（**C 代码**） | `apps/privacy-worker/Cargo.toml:14-31` |
| ORT 压缩包 | 8,309,231 B ≈ 7.9 MiB（x64 1.23.2 tgz） | `curl -sIL` 实测 |

结论：**纯正则模式零模型依赖、零 cgo**；本地模型模式要多带一个 Rust 二进制 + ONNX Runtime 动态库 + 37MiB 模型文件，且引入 C 依赖（oniguruma）。

### 3.6 检测前先"划清范围"

`core/internal/privacy/document.go` 不是把整个 body 当文本扫，而是按协议结构提取：

- `protocolRoots()`（`document.go:159-189`）：每个协议只扫指定根字段。
  - Chat: `messages`
  - Responses: `instructions` / `input` / `prompt`
  - Anthropic: `system` / `messages`
  - Gemini: `systemInstruction` / `contents`
  - Embeddings / Rerank / Images / Search 各有根
- `walkJSONStrings()`（`document.go:191-255`）：递归字符串，**跳过** base64 / image_url / 音频 / 文件（`skipJSONChild`，`document.go:347-433`）。
- `continuationPaths()`（`continuation.go:14`）：**排除推理签名/加密续传**（`signature`、`encrypted_content`、`redacted_thinking.data`），否则一改就 400。
- 工具声明默认不扫（`document.go:150-158` 注释：扫了会让占位符数量涨一个数量级，且保护不到任何东西）。

---

## 4. 三档策略（warn / block / redact）

### 4.1 动作是 4 值不是 3 值

`core/internal/privacy/types.go:26-33`：`allow` / `redact` / `block` / `warn`。
契约层：`contract/policy.go:16-21` `PolicyAction`。

### 4.2 判定点：`Engine.Inspect`

`core/internal/privacy/engine.go:86-204`，流程：

1. `extractDocument` 抽 segment（`engine.go:97`）
2. detector 检测（`engine.go:122-129`）
3. `normalizeFindings` 校验边界/UTF-8（`engine.go:130`、`normalizeFindings:313-339`）
4. `partitionFindings` 过滤（`engine.go:135`、`suppressionFor:231-264`）
5. **按 action 分支**（`engine.go:143-203`）

### 4.3 三档各自的实现

| 动作 | 代码 | 行为 |
| --- | --- | --- |
| **warn** | `engine.go:144-152` | 不改 body；返回 `DecisionWarn` + `Notified` + `Fingerprints` |
| **block** | `engine.go:153-159` | 不改 body；返回 `Protected` 明文列表（给记录做遮罩） |
| **redact** | `engine.go:160-200` | 分配占位符 → `rewriteDocument` → 返回新 body + `Redactions` |

warn 的落地在 ingress（`handler.go:608-637`）：

- 设置**响应头** `X-AstrLink-Policy-Warning: email=2,phone=1`（`handler.go:626`，常量 `handler.go:187`）。
- 注释强调这个头不能进 `request.Header`（会跨上游边界）。
- 同时 `ReportPolicyWarning` 喂给内存 `WarningFeed`（`warning_feed.go:58-110`），桌面轮询 `/control/v1/privacy/warnings`（`controlapi/privacy_warnings.go:13`）。
- **去重**：同一值用 keyed fingerprint 记住 16384 个（`warning_feed.go:16-20`），避免 agent 每轮重发历史刷屏。

block 的落地：`handler.go:645` 返回 `errPrivacyBlocked` → `handler.go:779-781` 映射成 **HTTP 403 `policy_blocked`**。

### 4.4 失败语义（容易抄漏）

`handler.go:752-786` 是显式的错误映射表，**绝不静默放行**：

| 情况 | HTTP | code |
| --- | --- | --- |
| 策略取不到 | 503 | `privacy_policy_unavailable` |
| 检测引擎不可用 | 503 | `safety_engine_unavailable` |
| body 无法安全解析 | 422 | `privacy_inspection_failed` |
| 改写后无法保证合法 | 422 | `privacy_redaction_failed` |
| 命中 block | 403 | `policy_blocked` |

### 4.5 抑制规则（误伤治理，全部在一个函数里）

`engine.go:231-264` `suppressionFor`，按顺序：

| 原因 | 常量 | 说明 |
| --- | --- | --- |
| 该 kind 关闭 | `SuppressionKindDisabled` | 逐 kind 开关 |
| 置信度低 | `SuppressionLowConfidence` | 仅模型模式，阈值默认 0.80（`contract/policy.go:47`） |
| 已经是占位符 | `SuppressionPlaceholder` | 防止占位符套娃（`placeholders.go:303-374`） |
| 命中白名单 | `SuppressionAllowlisted` | 见下 |
| 示例值 | `SuppressionExampleValue` | 支付测试卡号等（`example_values.go`） |
| 自家 ID | `SuppressionAstrLinkIdentifier` | `req_`/`session_`/`access_token_` 等 `astrlink_identifier.go:20-42` |

**白名单**（`allowlist.go:42-69` + `contract/policy.go:237-252`）：

- 3 种类型：`literal` / `domain_suffix` / `cidr`。
- 默认种子：`localhost`、`github.com`、`githubusercontent.com`、`example.com/.net/.org`、`test`、`invalid`、回环/私网/链路本地 CIDR。
- 智能点：`domain_suffix` 一条规则同时覆盖裸域名、URL、邮箱域（`allowlistHost`，`allowlist.go:71-84`）；`cidr` 也能匹配 URL 里的裸 IP（`allowlistAddress`，`allowlist.go:88-97`）。

**通用密钥的启发式**（`generic_secret.go:1-70`）：

- 必须有数字 + Shannon 熵 ≥ 3.0 bit/char + 不含 stopword（`example`/`dummy`/`your`/`test`…）+ 不是 `process.env.X` 这类引用路径。

---

## 5. 占位符方案

### 5.1 两种形态

**token 型**（默认，`document.go:826-851` `replacementFor`）：

```
<PRIVATE_EMAIL_b299548153dfc50a>
<SECRET_...>
<PRIVATE_PAYMENT_CARD_...>
```

**natural 型**（仅 email / url / ip_address 允许，`contract/policy.go:134-168` `PlaceholderStyleLocked` 锁死其余类型）：

| Kind | 形态 | 证据 |
| --- | --- | --- |
| email | `redacted-<12hex>@private.invalid` | `placeholders.go:235-237` |
| url | `https://private.invalid/r/<12hex>` | `placeholders.go:238-240` |
| IPv4 | RFC 5737 三段测试网共 768 个地址 | `placeholders.go:246-278` |
| IPv6 | `2001:db8::` + 派生后缀 | `placeholders.go:253-266` |

用 `.invalid` 而不是 `example.com`：后者 IANA 真在运营，agent 会真去请求（`placeholders.go:224-229` 注释与 229 行常量）。

### 5.2 一致性怎么保证

**派生而非随机**（`placeholders.go:202-215`）：

```
suffix = HMAC-SHA256(key, kind || 0x00 || value || 0x00 || trial)[:16]   // token
suffix = HMAC-SHA256(key, kind || 0x00 || value || 0x00 || trial)[:12]   // natural
```

- 同一 `(kind, value)` 永远得到同一占位符 → 多轮对话内部自洽，**上游 prefix cache 不会因为第一处脱敏就整段失效**（`placeholders.go:24-34` 注释）。
- 用 HMAC 不用裸 SHA256：邮箱/IP 裸哈希可枚举反查（同上注释）。

**key 从哪来**：

- 无持久化：进程内随机 32 字节（`placeholders.go:35-41`），重启后所有占位符换名。
- 有持久化：`HKDF(auditKey, info="astrlink/privacy-placeholder/v1")`（`engine.go:41-60` `PersistPlaceholders`）。
  `auditKey` 来自存储（`store.GetAuditKey`，`cmd/astrlink-core/persistent.go:192-197`），桌面下由 KEK 保护。

### 5.3 冲突怎么处理

`placeholders.go:45-63` 的注释把三类冲突写得很清楚，实现：

| 冲突 | 处理 | 代码 |
| --- | --- | --- |
| 两个不同明文派生出同一后缀 | trial 0..63 递增重派生 | `placeholders.go:136-144` |
| 候选占位符**已出现在本请求 body 里** | 认为是上一轮未还原的占位符 → **复用**（重派生会破坏 prefix cache） | `placeholders.go:176-200` |
| 有限命名空间用尽（IPv4 池 768 个） | 回退 token 型，并标记 `SuppressionUnrepresentable` | `placeholders.go:109-119`、`engine.go:284-294` |

IPv4 特殊：池子小到"候选地址可能是别人的占位符"，所以引入 **per-conversation ledger**（`standin_ledger.go`）：

- 按 `owner = HMAC(key, kind||value)[:32]` 记「谁领了这个地址」（`standin_ledger.go:144-173`）。
- 只记哈希不记明文（`standin_ledger.go:33-40`）；有 store 就持久化，重启后仍一致（`standin_ledger.go:104-141`）。
- LRU 上限 1024 个会话（`standin_ledger.go:31`）。

### 5.4 系统提示注入

`notice.go:15-60`：**只有 token 型**才需要告诉模型"这是不透明标记，原样复制"；natural 型本身就是合法值，模型会自然照抄。
注入位置是协议各自的 system 通道（`notice.go:44-70`），且会去重（客户端已经贴过就不再注入）。

---

## 6. 响应还原 + SSE 流式

这是全项目最复杂的部分，分三层。

### 6.1 第一层：`TextRestorer`（单段文本，跨 chunk 拼接）

`core/internal/privacy/restore_forms.go:18-45`。

模型不会逐字节复述占位符，会按当前输出语法转义。所以每个占位符生成**多种 spelling**（`restore_forms.go:142-172`）：

| spelling | 例 | 用途 |
| --- | --- | --- |
| 原样 | `<PRIVATE_EMAIL_ab12...>` | 纯文本 |
| 去尖括号 | `PRIVATE_EMAIL_ab12...` | Markdown 吞标签 |
| HTML 实体 | `&lt;PRIVATE_EMAIL_...&gt;`（含 `&#60;` `&#x3c;`） | HTML 页面 |
| `\u003c` 转义 | `\u003cPRIVATE_EMAIL_...\u003e` | JSON 内嵌 |
| 百分号 | `%3cPRIVATE_EMAIL_...%3e` | URL |

- 匹配用**前缀索引 + 二分**加速（`restore_index.go:30-97`），普通 `<` 文本不会触发全部匹配器。
- 还原值按目标语法编码：`ValuePlain` / `ValueJSONString`（`restore_forms.go:12-20`、`EscapeJSONStringContent:174-190`）。
- **`hold` 语义**（`restore_forms.go:90-95`）：输入结尾恰好是某个占位符的前缀时，不吐出这段（`hold = len(input)-at`），等下一个 chunk 拼上来。这是流式不裂开的关键。

### 6.2 第二层：`visibleRestoreEngine`（协议感知的帧队列）

`core/internal/ingress/protocol_restore.go:86-160`。

- 只把**完整的协议帧**入队（SSE 一帧、JSON 一个值）。
- 按**语义频道**聚合文本：`chat:delta:0`、`response.output_text.delta:item:content`、`completion:0`……（`protocol_restore.go:489-520`、各 `append*Refs`）。
  这样 choice 0 的半个占位符不会吃到 choice 1 的文本。
- 判断频道是否关闭（`closedRestoreChannels`，`protocol_restore.go:379-404`）：`finish_reason` 出现、`response.output_text.done`、Anthropic `message_stop` 等。
  频道关了 → `final=true`，不再 hold。
- **失败可回退**：`abort()`（`protocol_restore.go:160-177`）把队列里的**原始字节**原样吐出，保证"宁可没还原，不可写坏"。
- 上限：256 KiB / 128 帧（`protocol_restore.go:17-18`），超限即 abort。

### 6.3 第三层：`restoringResponseWriter`（HTTP 写出）

`core/internal/ingress/restore_writer.go`。

`WriteHeader`（`restore_writer.go:72-113`）决定走哪条路：

| 条件 | 行为 | 行 |
| --- | --- | --- |
| 状态码 ≥ 400 | **passthrough**（错误体是协议数据，不改） | 78-84 |
| `Content-Encoding` 非 identity | **502 `upstream_content_encoding`** | 85-93 |
| Content-Type 不认 | passthrough | 94-100 |
| 非流式 | `restoreModeBuffered` | 101-105 |
| `text/event-stream` | `restoreModeSSE` | 116-118 |
| `text/plain` | `restoreModeText` | 119-120 |
| 其它 JSON 流 | `restoreModeJSONStream` | 121-122 |

- SSE：按帧边界切（`nextSSEFrameEnd`），`[DONE]` 走 `terminal()`（`restore_writer.go:245-283`）。
- 非流式：整段缓冲，超过 8 MiB（`restore_writer.go:16`，`maxResponseInspectionBytes = 8<<20`）就放弃还原直通（`restore_writer.go:156-176`）。
- 统计：`privacyRestoreSummary` 记录 mapping/restored/fallback/hits（`restore_writer.go:453-466`），落到请求记录。

### 6.4 上游必须禁压缩

`core/internal/ingress/execution.go:616`：只要本次请求有 redactions，就 `attemptRequest.Header.Del("Accept-Encoding")`。
否则上游返回 gzip，还原路径直接 502。

### 6.5 只还原"给人看的"，工具参数单独开

`protocol_restore.go:236-260`：

- `visibleResponseTextRefs` 收可见文本（`protocol_restore.go:489`）。
- `toolArgumentTextRefs`（`protocol_restore_tools.go:19`）只在 `policy.RestoreToolArguments` 开启时才收。
- 原因：把真实密钥写进模型的工具调用参数，等于让 agent 拿着明文去打 API。这是产品级取舍，不是技术限制。

---

## 7. 请求链路与日志

### 7.1 拦截点

不是反向代理，是 gateway 的 attempt 循环内：

```
选 channel → 每个 candidate → applyPrivacy() → 发上游 → restoringResponseWriter → 客户端
```

- 调用点：`core/internal/ingress/execution.go:313-330`（**每个候选 channel 都跑一次**，但注释说"privacy filter applies once, to the client's request"）。
- 实现：`core/internal/ingress/handler.go:541-700` `applyPrivacy`。
- body 缓冲：`handler.go:794-834` `bufferPrivacyBody` → 全读进内存 → redact 后 `buffered.Replace(result.Body)`（`handler.go:653`）。
- 并发闸：`DefaultMaxConcurrentInspections = 16`，范围 4–128（`handler.go:151-153`、`handler.go:165-171`）。
- 大小限制：`-max-request-body-mib`；压缩体解码后硬上限 256 MiB（`main.go:80-84`）。

### 7.2 日志脱敏：没有独立"日志清洗器"

AstrLink 的"日志"= SQLite 请求记录，策略是**四层防线**而不是事后清洗：

| 层 | 做法 | 证据 |
| --- | --- | --- |
| 1. 默认不存正文 | `request_body_enabled: false`、`response_content_enabled: false` | `contract/audit_settings.go:50-61` |
| 2. 存了也加密 | 正文 AES-GCM 密封成 `AuditBlob` | `ingress/request_record.go:1641-1652` |
| 3. 打可见性标签 | `shareable` / `raw` / `pending`，redact/block 后客户端 body 标 `raw` | `request_record.go:1659-1689` |
| 4. 预览单独遮罩 | `maskProtected` 把命中值换成 `…`，再 `sanitizePreview` 切标题 | `request_record.go:1738-1747`、`session_text.go:97-112` |

细节：

- 记录里只存**路径**不存值：`Finding.Path` 是 JSON pointer（`types.go:163-180` 注释）。
- `sanitizePreview`（`session_text.go:76-92`）另外用正则洗 URL/密钥，不管有没有命中隐私策略。
- 没有发现"把 body 打到 stderr/日志文件"的代码路径（`handler.go` 内无 body 日志）。

---

## 8. 依赖重量

### 8.1 Go 侧

`core/go.mod` 直接依赖 16 个（另 17 个 indirect），关键几个：

| 依赖 | 用途 |
| --- | --- |
| `github.com/tidwall/gjson` / `sjson` | JSON 路径读写（new-api 已有） |
| `modernc.org/sqlite` | **纯 Go** SQLite（无 cgo） |
| `github.com/expr-lang/expr` | 路由/规则表达式（与隐私无关） |
| `github.com/zalando/go-keyring` | 桌面凭据（隐私无关） |
| `github.com/QuantumNous/new-api/relaykit` | 协议转换（AGPL！） |

- **无 cgo**：`grep -rn '^import "C"' --include='*.go'` 无结果；`Dockerfile:43` `CGO_ENABLED=0`。
- **实测二进制**：`CGO_ENABLED=0 go build -trimpath ./cmd/astrlink-core` → **37.2 MB**（本机 linux/amd64，含 webui 未启用）。
  命令与结果：`ls -lh /tmp/astrlink-core-bin` → `37.2M`。

### 8.2 模型侧（仅 local_model 模式需要）

| 件 | 体积 |
| --- | --- |
| Rust worker 二进制 | 未验证（未编译） |
| `libonnxruntime.so.1.23.2` | 未验证（压缩包 7.9 MiB） |
| 模型权重 INT8 | 37.2 MiB（`astrlink_guard.go:105`） |
| 模型 FP32 | 142 MiB（`astrlink_guard.go:110`） |

---

## 9. 可直接移植的包 / 文件

| 包 / 文件 | 非测试 LOC | 依赖是否自包含 | 建议 |
| --- | --- | --- | --- |
| `core/internal/privacy/`（24 文件） | **5069** | 依赖 `contract`(51 个符号)、`storage`(5 个接口类型)、`jsonedit`、`systemprompt`、`gjson` | **直接抄，主要工作量** |
| `core/internal/jsonedit/` | 121 | 自包含（gjson + sjson） | 直接抄 |
| `core/internal/systemprompt/` | 378 | 自包含 | 直接抄（注入提示用） |
| `core/contract/policy.go` | 662 | 类型 + 校验，自包含 | 抄类型定义，改包名 |
| `core/contract/privacy_model.go` | 607 | 模型契约 | 二期再抄 |
| `core/internal/privacyworker/` | 2305 | 需 Rust worker + ONNX + `os/exec` | 二期，可选 |
| `core/internal/privacymodel/` | 8123 | 模型下载/校验/管理，桌面产物重 | **不建议抄** |
| `core/internal/ingress/restore_writer.go` + `protocol_restore*.go` | 1632 | 强依赖 `net/http` + 自家协议层 | **重写**，不要复制 |

### 移植时的替换点

| AstrLink | new-api 替换成 |
| --- | --- |
| `contract.ProtocolID`（`ProtocolOpenAIChat` 等） | new-api 的 relay format / `constant.RelayMode` |
| `storage.PrivacyStandInStore` | 自己的 `model` 层（Redis 或 DB） |
| `storage.PolicyStore` | 新的策略表（多租户，见风险 1） |
| `privacyworker`（本地模型） | 可先不做，只上 regex 模式 |
| `net/http.ResponseWriter` | `gin.ResponseWriter` |

---

## 10. 桌面端耦合、不能直接用的部分

| 模块 | 为什么不能直接搬 |
| --- | --- |
| `apps/desktop/src-tauri/src/tray.rs`（104 KB）、`sidecar.rs`（330 KB） | 托盘、窗口、sidecar 生命周期 |
| `kek_store.rs`、`core/internal/localkey` | 桌面 KEK（keyring / stdin），服务端是文件 |
| `raw_approval.rs`、`raw_biometric.rs`、`raw_key_pin.rs` | 生物识别审批，纯桌面 |
| `macos_widget*.swift`、`os_lock.rs` | 锁屏即锁定明文访问 |
| `WarningFeed` + `/control/v1/privacy/warnings` | 靠桌面轮询弹通知；服务端要换成 SSE/WebSocket 或直接砍掉 |
| `privacyworker.SiblingExecutablePath()` | 假定 worker 与 core 同目录（`cmd/astrlink-core/persistent.go:168-171`） |
| `-parent-pid` 父进程监控 | 桌面专属 |
| `apps/classifier-worker` | 自动分类/自动模型，与隐私检测无关 |

---

## 11. 四个重点问题的答案

### 11.1 流式响应还原

**核心机制是"hold + 频道"**：

1. 每个 spelling 匹配失败但可能是前缀时，`Restore` 返回 `hold>0`，调用方把尾巴留着（`restore_forms.go:90-95`）。
2. 上层 `visibleRestoreEngine` 只看完整协议帧，按语义频道拼文本；频道未关闭就继续等（`protocol_restore.go:236-260`）。
3. 频道关闭（`finish_reason` / `done` 事件）→ `final=true` → 不再 hold。
4. 任何解析失败 / 超限 → `abort()` 原样吐字节（`protocol_restore.go:160-177`）。
5. SSE 按帧切，`[DONE]` 特殊处理；非 SSE 的 JSON streaming 按"一个完整 JSON 值一帧"切（`restore_writer.go:311-395`）。

**坑**：hold 会让首字节延迟。AstrLink 用"频道关闭即 final"来限住，但最坏情况要等整个 `delta` 流结束。

### 11.2 占位符一致性 / 冲突

- 一致性靠 **HMAC(key, kind, value)**，不是随机 ID；同值同占位符，跨轮次稳定（`placeholders.go:202-215`）。
- key 必须持久化，否则重启全变（`engine.go:49-60`；`persistent.go:192-197`）。
- 冲突三态：重派生 / 复用 / 回退 token（`placeholders.go:45-63`）。
- IPv4 池只有 768 个，用 per-conversation ledger 防串号（`standin_ledger.go`）。
- 已被脱敏过的值再次出现时靠 `isNaturalPlaceholder` / `isTokenPlaceholderSpan` 跳过（`placeholders.go:303-374`），否则会套娃且内层永远还原不出来。

### 11.3 误伤与白名单

- 默认只开 `common_secret`（`contract/policy.go:168-179`）。
- 白名单三类型 + 默认 15 条（`contract/policy.go:237-252`）。
- 6 类抑制原因全部集中在一个函数（`engine.go:231-264`），dry-run 能看到"为什么没脱敏"。
- 通用密钥启发式：熵 ≥ 3.0、必须含数字、排除 stopword 和 `process.env.X` 引用（`generic_secret.go:20-70`）。
- 结构化 JSON 里的**数字**替换时加引号，避免把 JSON 写坏（`document.go:815-819`、`structured_json.go:1-10`）。

### 11.4 性能开销

实测（本机 N6000 1.1GHz，偏慢，服务器上应更快）：

| 基准 | 结果 | 命令 |
| --- | --- | --- |
| `extractDocument` 1 MiB（含 base64 图片跳过） | 4.99 ms，210 MB/s，47 allocs | `go test ./internal/privacy -bench BenchmarkExtractDocumentSkippedImage` |
| `extractDocument` 8 MiB | 35.7 ms，235 MB/s | 同上 |
| `TextRestorer` 1.7 MB 文本 / 10 映射 | 2.76 ms，101 MB/s，**0 alloc** | `BenchmarkTextRestorerCode` |
| 同上 / 1000 映射 | 2.90 ms，96 MB/s | 同上 |
| `NewTextRestorer` 1000 映射 | 12.3 ms，5.3 MB，40123 allocs | `BenchmarkNewTextRestorer` |

结论：**每请求固定成本** ≈ body 大小 × 4.3 ms/MB（解析）+ 响应文本 × 10 ms/MB（还原）+ 每个请求重建 restorer 的 12 ms 级开销（1000 映射时）。
高 QPS 网关上是实打实的 CPU 成本，`NewTextRestorer` 的构建成本尤其要注意（可以按 redaction 集合缓存）。

---

## 12. 移植到 new-api 的 5 个主要风险

### 风险 1（最高）：策略是单例，new-api 是多租户

`StorePolicyProvider.RequestPolicy(ctx, _ Scope)` 直接忽略 Scope，永远读 `DefaultPrivacyPolicyID`（`policy_store.go:22-28`）。
new-api 有 user / token / group / channel 四个维度，"谁被脱敏、脱敏到什么程度"必须重新设计。
接口留了口子（`PolicyProvider` 带 `Scope`，`types.go:90-105`），但实现层是空的。

### 风险 2：流式还原要接进 6+ 条写出路径

new-api 的 SSE 写出分散在 `relay/helper/common.go:238`（`StringData`）、`common.go:222`（`ResponseChunkData`）、`common.go:198`（`ClaudeChunkData`）、各 channel adaptor 的 `HandleStreamResponseData`……
AstrLink 只需包一个 `http.ResponseWriter`，因为它是自己写响应；new-api 是每个 handler 自己 `c.Render`。
**要么在 `SetEventStreamHeaders` 处统一包 `c.Writer`（`relay/helper/common.go:179-194` 已经这么干了），要么逐条改。漏一条 = 那条路径不还原或写坏 JSON。**

### 风险 3：多实例部署时占位符 key 不一致

AstrLink 是单机单进程（桌面 / 单容器），key 在内存或本地 SQLite。
new-api 常见多副本 + Redis。如果 key 每实例不同：
- 同一会话第 2 轮打到别的实例 → 占位符换名 → 上游 prefix cache 全废；
- 响应还原用的是发起请求那台的 mapping，尚可，但**跨轮次的 IPv4 ledger 会串**。
必须把 key 和 ledger 放到共享存储（Redis/DB），并保证 HKDF 的 auditKey 全局一致。

### 风险 4：上游压缩与重试路径

- 必须在有 redaction 时禁 `Accept-Encoding`（AstrLink 在 `execution.go:616` 做），否则还原层直接 502。
- new-api 有 `middleware/gzip.go` 会压响应，压缩后的 body 不能再改；顺序必须是「还原 → 压缩」。
- 重试/failover 时 body 会被重新读取（`controller/relay.go:194` `GetBodyStorage`），要保证**只脱敏一次**（AstrLink 用 `buffered.Replace` 保证幂等，`handler.go:653`）。

### 风险 5：误伤在多租户下代价更高

AstrLink 是"自己的请求自己负责"；new-api 是给别人用的网关。
- 一个邮箱被 `<PRIVATE_EMAIL_...>` 替换，调用方的下游解析/断言就可能挂。
- 工具参数被替换成 token 标记会破坏 JSON Schema 校验（AstrLink 只对 natural 型缓解，且 natural 只允许 email/url/ip）。
- 建议：默认 `allow`，按 user/group 灰度开启；warn 模式先跑一周看命中率。

---

## 13. 许可注意事项

### 13.1 现状

| 对象 | 许可 | 证据 |
| --- | --- | --- |
| AstrLink 自有源码 | **Apache-2.0** | `LICENSE:1-3` |
| AstrLink core 链接的 RelayKit | **AGPL-3.0** | `LICENSING.md`（中英文两版） |
| 组合后的 core | **整体按 AGPL-3.0 分发** | `LICENSING.md` §"自有源码与 RelayKit" |
| new-api | **AGPL-3.0** | `.refs/new-api/LICENSE:1-2` |

### 13.2 Apache-2.0 需要保留什么（如果只抄 `privacy` 包）

1. **版权声明**：保留原文件头（本仓库文件头大多没有 copyright 行，但 `NOTICE` 若有必须带）。本仓库根目录**没有 NOTICE 文件**（`ls` 确认），所以只需保留 `LICENSE`。
2. **许可证副本**：分发时附 Apache-2.0 全文；在 `THIRD-PARTY-LICENSES.md`（new-api 已有，33 KB）里加一条 AstrLink。
3. **标明修改**：Apache-2.0 §4(b) 要求对修改过的文件"carry prominent notices stating that You changed the files"。
   实践：在每个改过的文件头加一行 `// Modified from AstrLink (Apache-2.0): <简述>`。
4. **不得使用其商标**：Apache-2.0 §6 不授予商标权，别把 "AstrLink" 写进 new-api 的产品名/宣传。

### 13.3 关键警告：不要抄 `core/internal/relaykitbridge/`

`core/internal/relaykitbridge/`（2224 行）是 AstrLink 与 RelayKit 的桥接层，属于 AGPL 组合体的一部分（`LICENSING.md` 明确"不为 RelayKit 授予 Apache-2.0 替代许可、链接例外"）。

**但**：目标 new-api 本身就是 AGPL-3.0，所以即使抄了也不产生新的许可冲突——只是不能对外宣称"这段是 Apache-2.0"。

### 13.4 若 new-api 未来要双许可 / 闭源分发

那 `privacy` 包（Apache-2.0）是安全的，`relaykitbridge` / `ingress` 相关部分是危险的。**建议只搬 `core/internal/privacy` + `jsonedit` + `systemprompt` + `contract/policy.go`，这四块都在 AstrLink 自有源码范围内。**

---

## 14. 未验证事项

| 项 | 原因 |
| --- | --- |
| Rust worker 二进制体积 | 未编译（需要 cargo + ORT） |
| `libonnxruntime.so` 解压后体积 | 只查了 tgz 的 Content-Length（7.9 MiB） |
| 桌面安装包总体积 | 未构建 Tauri |
| new-api 全部 SSE 写出路径是否都经过 `SetEventStreamHeaders` | 只抽查了 `relay/helper/common.go` 与 claude adaptor |
| `privacy` 包在新-api 的 Go 版本（1.26.10）下能否原样编译 | 未做（AstrLink 用 go 1.26.10，new-api 同版本，风险低） |
| 模型在真实中文 prompt 上的召回/误报 | 未跑评测 |
| AstrLink 是否有请求体明文落盘的其他路径（如崩溃 dump） | 只搜了 `ingress`/`transport` 的日志调用 |

---

## 15. 建议的落地顺序（供决策参考，非本次任务范围）

1. **第一步（1–2 天）**：只搬 `privacy` + `jsonedit` + `systemprompt` + `contract/policy.go`，先做 **regex 模式 + block/warn**，不碰还原。接入点选在 `controller/relay.go` 的 `GetBodyStorage` 之后。
2. **第二步（3–5 天）**：加 redact + 非流式还原（`ShouldRestoreContentType` 那套），先只覆盖非流式请求。
3. **第三步（1 周+）**：SSE 还原。先在 `SetEventStreamHeaders` 处统一包 `c.Writer`，只覆盖 chat/completions，验证通过再扩协议。
4. **第四步（可选）**：本地模型（Rust worker + ONNX）。收益是能识别人名/地址/日期，代价是多一个二进制 + 37 MiB 模型 + C 依赖。

---

*报告生成时间：2026-11-05。所有行号基于 `19fe789`。*
