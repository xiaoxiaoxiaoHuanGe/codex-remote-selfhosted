# 贡献指南

感谢关注 Codex Remote。欢迎通过 Issue、讨论和 Pull Request 改进项目。

## 开始之前

1. 先搜一遍 [Issues](https://github.com/yunyuchen/codex-remote/issues)，避免重复。
2. 较大改动（协议变更、安全模型、新依赖）请先开 Issue 说明动机与方案。
3. 请勿在公开 Issue / PR 中粘贴真实 token、私钥、中继配置或完整漏洞利用。

## 开发环境

### 桥（Go）

```bash
cd bridge
go test ./...
go run ./cmd/codexbridge serve -addr 127.0.0.1:8767 -dev-random-token
```

### App（Flutter）

```bash
cd app
flutter pub get
flutter test
flutter run -d <device> \
  --dart-define=WS_URL='ws://127.0.0.1:8767/ws?token=<token>'
```

### 协议 Schema

`protocol/` 由 Codex app-server 导出，修改前请确认对应 CLI 版本：

```bash
codex app-server generate-json-schema --out protocol
```

## 分支与提交

- 从最新 `main` 拉分支：`feat/...`、`fix/...`、`docs/...`、`chore/...`
- 提交信息建议约定式：`feat(bridge): ...`、`fix(app): ...`、`docs: ...`
- 保持 PR 小而专注；安全相关改动请单独成 PR，并说明威胁模型影响

## 代码约定

- **密钥永不入库**：token、`frpc.toml`、agent env、安装包产物走环境变量 / 本地文件 / Releases
- **默认 fail-closed**：鉴权、路径解析、沙箱策略变更必须有对应测试
- **远程 turn 降权不可削弱**：不要引入可被客户端单方面关闭的“全自动远程执行”捷径
- Go：与相邻文件风格一致；导出符号需有清晰职责
- Flutter：UI 与 `services/` 分层；密钥只进 `flutter_secure_storage`
- 文档中的域名 / IP 使用占位符（`relay.example.com`、`203.0.113.10`）

## Pull Request 检查清单

- [ ] `go test ./...`（在 `bridge/`）通过
- [ ] 若改动 App：`flutter test` 通过
- [ ] 无真实密钥、个人路径、生产域名
- [ ] 用户可见行为变更已更新 README 或相关 docs
- [ ] 安全相关变更在 PR 描述中写明影响面

## 安全漏洞

请勿公开披露。按 [SECURITY.md](./SECURITY.md) 私下报告。

## 许可证

贡献代码即表示你同意以项目的 [MIT License](./LICENSE) 授权。
