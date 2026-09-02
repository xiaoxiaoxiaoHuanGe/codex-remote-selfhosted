# Codex Remote 模型列表动态同步报告

日期：2026-09-01
代码分支：`fix/dynamic-model-sync`

## 结论

已完成最小动态同步修复。手机 App 不再只依赖编译期模型字符串，而是在连接 Bridge 后请求 `models`，由 Bridge 调用当前机器上的 Codex app-server `model/list`，再把模型 ID、显示名称和每个模型支持的 reasoning effort 转发给 Flutter 选择器。

本机实测的 Codex Desktop/app-server 版本为 `0.151.0-alpha.7.2`。当前可用列表包含：

| 显示名称 | 实际 model id | 默认 effort | 可用 effort |
|---|---|---|---|
| GPT-5.6-Sol | `gpt-5.6-sol` | `low` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` |
| GPT-5.6-Terra | `gpt-5.6-terra` | `medium` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` |
| GPT-5.6-Luna | `gpt-5.6-luna` | `medium` | `low`, `medium`, `high`, `xhigh`, `max` |
| GPT-5.5 | `gpt-5.5` | `medium` | `low`, `medium`, `high`, `xhigh` |
| GPT-5.4 | `gpt-5.4` | `medium` | `low`, `medium`, `high`, `xhigh` |
| GPT-5.4-Mini | `gpt-5.4-mini` | `medium` | `low`, `medium`, `high`, `xhigh` |

因此 GPT-5.6 不是一个应被硬改成的单一字符串；当前实际对应的是三个 app-server model id，选择器现在会直接显示这三个真实条目。

## 第一阶段审计

### A. Flutter 当前模型列表来源

原来来自 [chat_composer.dart](D:/codex-remote/app/lib/src/ui/chat_composer.dart)：

- `gpt-5.5` → `GPT-5.5`
- `gpt-5` → `GPT-5`
- `gpt-5-mini` → `GPT-5 mini`

原实现没有从配置文件、Bridge 或 app-server 获取模型列表。

### B. 硬编码情况

存在两处旧模型硬编码：

1. Flutter `ChatComposer` 的 `_models`、`_modelShort` 和初始 `_lastModel`。
2. Bridge `server.go` 的 `sanitizeModel`，只接受 `gpt-5.5`、`gpt-5`、`gpt-5-mini`。

本次保留旧三项仅作为动态请求失败时的明确 fallback；它们不再作为成功同步时的模型来源。

### C/D. Bridge 与 app-server 模型枚举

仓库的协议 schema 已定义 `model/list`、`ModelListResponse`、`Model`、`supportedReasoningEfforts` 和 `defaultReasoningEffort`，但修改前 `bridge/internal/appserver/methods.go` 没有相应客户端方法，Bridge 也没有调用该 RPC，因此实际忽略了 app-server 的模型枚举。

本机直接启动 `codex.exe app-server` 后实测：

1. `initialize` 成功。
2. `model/list` 成功返回上述 6 个非隐藏模型。
3. 每个模型返回真实 ID、显示名、默认 effort 和可用 effort。

这证明 App 只显示 GPT-5.5 的直接原因是 Flutter 和 Bridge 双方的旧静态列表/白名单，而不是当前 Codex app-server 缺少 GPT-5.6。

### E. 当前 model 的传递路径

原路径为：

`ChatComposer` → `ChatScreen._onSend` → `BridgeClient.sendPrompt` 的 JSON `model` 字段 → Bridge `inbound.Model` → `handlePrompt` → `appserver.TurnStartParams.Model` → `turn/start`。

`thread/start` 本身没有传 model；模型选择是在 `turn/start` 的 model 字段生效。当前改动保留这条路径，只把 model 值改为 app-server 返回并校验过的真实 ID。

### F. reasoning effort

原来 Flutter 菜单固定为 `low`、`medium`、`high`、`xhigh`，Bridge 也固定按这组旧枚举过滤。实测新模型存在 `max`、`ultra` 差异。

现在 effort 由 `model/list` 的 `supportedReasoningEfforts` 驱动；Bridge 只在所选模型明确支持该值时转发，否则省略非法值。旧 Bridge/app-server 不支持动态请求时，仍使用旧四项 fallback。

## 修改文件

### `D:/codex-remote/bridge/internal/appserver/models.go`

新增 `model/list` RPC 参数、响应模型和分页读取方法。Bridge 请求默认可见模型，最多安全跟随 20 页，忽略未知的新字段以保持前向兼容。

### `D:/codex-remote/bridge/internal/bridge/models.go`

新增 Bridge 内部模型能力目录：

- 将 app-server 的 ID、displayName、defaultReasoningEffort、supportedReasoningEfforts 转成手机需要的轻量字段。
- 过滤隐藏模型。
- 保留旧三模型作为 app-server 不可用时的 fallback。
- 保留 `max`、`ultra` 等未来/新版本返回的 effort 字符串。

### `D:/codex-remote/bridge/internal/bridge/server.go`

- 新增客户端消息类型 `{"type":"models"}`。
- 调用当前 Bridge 所连接的 Codex app-server `model/list`。
- 成功返回 `{"type":"models","source":"app-server","data":[...]}`。
- 失败返回 `source:"fallback"`、错误说明和旧模型列表。
- 将静态 `sanitizeModel` 改为基于当前 app-server 模型目录的精确 ID 校验。
- 将 effort 校验改为按所选模型能力校验。
- 不改变 FRP、WSS、Token、QR、session 或 Bridge WebSocket 既有协议。

### `D:/codex-remote/app/lib/src/models/codex_model.dart`

新增 Flutter 模型目录对象和旧模型 fallback 定义。fallback 仅用于兼容旧 Bridge 或 app-server 查询失败，并在 UI 中标记。

### `D:/codex-remote/app/lib/src/services/bridge_client.dart`

- 连接 ready 和前台同步时请求 `models`。
- 解析动态模型列表并暴露给 UI。
- 保存 `modelSource`、刷新状态和错误信息。
- 对不支持 `models` 的旧 Bridge 仍回落到明确的 fallback 列表。

### `D:/codex-remote/app/lib/src/ui/chat_composer.dart`

- 模型菜单改为显示 Bridge 返回的模型。
- 当前选择仍使用真实 model id 发送。
- 模型变化时保留仍有效的 effort；如果 effort 对新模型无效，则使用该模型的默认 effort。
- 动态刷新失败时显示“备用列表/刷新失败”，不静默伪装成 app-server 当前列表。
- 同时把当前 Flutter SDK 已弃用的语音参数迁移到 `SpeechListenOptions`，不改变语音功能。

### `D:/codex-remote/app/lib/src/ui/chat_screen.dart`

让 composer 监听 Bridge 状态，使模型列表收到后立即更新。
同时将两处颜色透明度调用迁移到当前 SDK 的 `withValues` API，仅为通过静态检查，不改变视觉数值。

### 测试文件

- `D:/codex-remote/bridge/internal/bridge/models_test.go`
- `D:/codex-remote/app/test/codex_model_test.dart`

覆盖 app-server 模型 ID、隐藏模型过滤、GPT-5.6 effort（包括 `max`/`ultra`）和 fallback 解析。

## 动态同步流程与 fallback

```text
Codex app-server
    └─ model/list
        └─ codexbridge: models + capabilities cache
            └─ WebSocket: { type: "models", source, data }
                └─ Flutter BridgeClient
                    └─ ChatComposer 动态模型/effort 菜单
```

- 动态请求成功：`source = app-server`，使用真实返回列表。
- RPC 失败或返回空列表：`source = fallback`，使用旧三模型，并显示刷新失败提示。
- 当前 model 仍在新列表中：保留用户选择。
- 当前 model 不再可用：优先选择 app-server 标记的 default，否则选择返回列表第一项。
- 当前 effort 对新 model 不可用：切换到该 model 的 default effort，再无 default 时选该 model 的第一项。

当前已有旧默认选择 `gpt-5.5` 且它仍在 app-server 列表中，因此按“保留仍有效的用户选择”规则不会强制改掉它；用户可在模型菜单中看到并选择 GPT-5.6-Sol/Terra/Luna。

## 实测和测试结果

### app-server 实测

- `initialize`：成功。
- `model/list`：成功，返回 6 个可见模型。
- GPT-5.6 真实 ID：`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`。

### Bridge WebSocket 实测

使用实际运行的本地 Bridge，发送 `{"type":"models"}`：

- 返回 `source=app-server`。
- 返回 6 个模型。
- `gpt-5.6-terra` 出现在返回列表中，并携带 `low/medium/high/xhigh/max/ultra`。

随后先等待该 models 响应，再发送使用 `gpt-5.6-terra`、`medium` effort 的无工具 prompt：

- Bridge 返回 `promptAccepted`。
- 回复为 `MODEL_SYNC_OK`。
- 收到 `turn/completed`。
- Bridge 审计日志记录 `model=gpt-5.6-terra`、`effort=medium`、`tier=flex`。

### Flutter

- `flutter analyze`：通过，无问题。
- `flutter test`：通过，31 项。
- `flutter build apk --release`：成功。

APK： [app-release.apk](D:/codex-remote/app/build/app/outputs/flutter-apk/app-release.apk)
大小：约 67.1 MB
SHA-256：`2FE8C2918227FB5CF4366AF4FD8BB3B1E1F0BF406A4261A95AB246DADE8637AF`

Bridge 动态模型相关 Go 测试通过。全量 `go test ./...` 中仍有既有的 Windows `TestValidateCwd` 环境测试失败（测试通过 `HOME` 临时变量模拟用户目录，但 Windows 下 `os.UserHomeDir` 行为未按测试预期改变）；该失败与本次模型改动无关，本次未修改安全路径逻辑。

## 尚存风险

1. 模型列表是 Bridge 连接的 app-server 账号/版本所返回的列表，不是对所有账号都保证相同；不同账号可能看到不同可用模型。
2. 当前 model/list 返回结构已按实测和仓库 schema解析，但未来若 app-server 改变必需字段，Bridge 会进入 fallback。
3. 旧 Bridge 不认识 `models` 消息时，App 会显示明确的 fallback；要获得动态列表，Windows 端需部署本次修改后的 Bridge。
4. 用户在首次动态列表返回前极快发送的、仅存在于新列表中的旧缓存 model 会被 Bridge 安全地省略；正常 UI 在模型可选前会先完成同步。
5. Flutter 构建仍提示未来需要升级 Gradle 9.1+、AGP 9.0.1+、Kotlin 2.3.20+；本次没有升级无关构建依赖。当前 APK 已成功构建。

## 后续升级建议

不建议为了本次 GPT-5.6 问题立即批量升级 Gradle、AGP、Kotlin 或 Flutter。动态同步已经解决了版本跟随问题；建议在单独分支、完成兼容性评估和完整真机回归后，再处理 Flutter 给出的未来版本提示。
