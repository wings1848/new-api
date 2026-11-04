# 多协议入口验收与接入说明（issue #30）

> 验收日期：2026-11-05；代码 `main@81963e712`；可重复执行脚本：`e2e/multi-protocol/run.sh`。

## 1. 结论

四个协议入口全部可用，验收 **9/9 通过**：

| # | 协议 | 请求 | 结果 |
|---|---|---|---|
| 1 | OpenAI Chat Completions | `POST /v1/chat/completions`（非流式） | ✅ 200，正常返回 `choices` |
| 2 | OpenAI Chat Completions | `stream:true` | ✅ 200，SSE 分块 `data:` + `[DONE]`，跨 chunk 文本正常 |
| 3 | OpenAI Chat Completions | `tools` 工具调用 | ✅ 200，透传 `tool_calls`（`get_weather`） |
| 4 | OpenAI Chat Completions | 多模态图片输入（`image_url`） | ✅ 200，图片内容不阻塞转发 |
| 5 | OpenAI Responses | `POST /v1/responses` | ✅ 200，返回 `output` |
| 6 | Anthropic Messages | `POST /v1/messages` + `anthropic-version` | ✅ 200，返回 `content[].text` |
| 7 | Gemini | `POST /v1beta/models/{model}:generateContent?key=TOKEN` | ✅ 200，返回 `candidates` |
| 8 | 错误透传 | 上游 429 | ✅ 客户端收到 429 + 上游错误体 |
| 9 | 错误透传 | 上游 500 | ✅ 客户端收到 500 + 上游错误体 |

## 2. 如何复现

```bash
# 需要 go / python3 / curl；会构建二进制并起临时 SQLite 实例（端口 18081/19290）
./e2e/multi-protocol/run.sh
# 输出每项 PASS/FAIL 和 SUMMARY n/9 passed
```

脚本做的事：
1. 构建 `wings-api` 二进制，临时目录启动（SQLite）
2. 起 mock 上游（`e2e/multi-protocol/mock_upstream.py`，同时模拟 4 种协议）
3. 通过管理 API 完成初始化、建 3 个渠道（OpenAI/Anthropic/Gemini）、建令牌
4. 跑 9 个用例并断言
5. 退出时清理进程与临时目录

## 3. 接入差异与坑

| 协议 | 鉴权 | 注意点 |
|---|---|---|
| OpenAI Chat | `Authorization: Bearer <token>` | 流式以 `data: [DONE]` 结束；客户端需处理文本被拆到多个 chunk |
| OpenAI Responses | 同上 | 走 OpenAI 渠道的 responses 能力；`input` 可为字符串或数组 |
| Anthropic Messages | `x-api-key: <token>`（Bearer 也接受）+ **必须** `anthropic-version` | 响应为 `type:message`，工具调用是 `tool_use` block |
| Gemini | `?key=<token>` 或 `x-goog-api-key` | 路径是 `/v1beta/models/{model}:generateContent`，流式是 `:streamGenerateContent` |

通用：网关按 **模型名** 路由到渠道，渠道 `models` 必须包含该模型；渠道 `group` 与用户/令牌分组需一致。

## 4. 未覆盖项（已开 #38）

- OpenAI Responses / Anthropic / Gemini 的**流式**变体
- 工具**结果回传**（`role:tool` / `function_call_output` 再请求）
- 4xx 参数错误 vs 上游 5xx vs 429 的细分断言
- 真实厂商 key 回归（reasoning 字段、缓存 token 计费）

## 5. 结论

多协议入口**不需要开发新协议适配**，后续 P1 工作应聚焦报表（#31）、防滥用（#32）、渠道换号（#33）；协议层只做 #38 的 e2e 补齐。
