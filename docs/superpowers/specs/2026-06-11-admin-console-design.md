# Web 后台管理设计（卡密运营 + 中继状态）

> 日期：2026-06-11
> 范围：给 codex-remote 的订阅控制面（`internal/license`，见 2026-06-10 订阅设计）配一个 Web 后台，替代 SSH + `codexhub admin` CLI 的日常运营；外加中继实时状态仪表盘。
> 技术路线（已确认）：**RuoYi-Vue 开源后端原样部署 + ele-admin-plus-ruoyi-ts-pro 前端模板 + codexhub 内部 admin API + RuoYi 侧 Java 薄代理**。
> 关系文档：`docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md` 是数据模型与授权语义的权威；本文只新增管理通道，不改授权语义。

---

## 1. 已确认的关键决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 管理范围 | 卡密/订阅运营 + 中继状态仪表盘 | 把 CLI 五个动作搬上网页，加在线可视化；不做全平台 |
| 管理员 | 单管理员（RuoYi 内置 admin 账号） | 只有运营者一人使用；不建多角色体系 |
| 发卡方式 | 纯手动（后台点按钮、复制发货） | 不留对外发卡 API |
| 后端形态 | RuoYi-Vue 原样部署，框架功能零开发 | 模板与其接口预先对接；登录/验证码/操作日志/管理员白拿 |
| 业务数据通道 | **hub 开内部 admin API（仅回环监听），Java 做无状态代理** | 卡密数据所有权不动（hub 的 SQLite + 内存）；MySQL 只存 RuoYi 框架数据 |
| 仓库布局 | 本仓库只放增量：`admin/web`（前端）+ `admin/ruoyi-ext`（Java 模块/SQL/配置样例） | 不把 RuoYi fork 塞进 monorepo；部署文档锁定上游 tag |

## 2. 非目标

- 对外发卡/支付 API（发卡平台对接后置）
- 多管理员、角色细分（RuoYi 体系留着但不配置）
- 用量计量、流量统计、日志检索
- 修改 `internal/license` 的授权语义或 `server.go` 的安全钳制

## 3. 总体架构

```
浏览器 ──HTTPS──> nginx ──┬── admin 前端静态文件（admin/web 构建产物）
                          └── /prod-api ──> RuoYi-Vue 后端 (JVM :8080)
                                              │ 框架功能 → MySQL + Redis
                                              │ /caret/** → Caret 代理模块
                                              ▼ HTTP + Bearer 共享密钥
                                  codexhub 内部 admin API (127.0.0.1:8768)
                                              │ 同进程直调
                                  license.Service (SQLite) + hub 内存连接表
```

职责边界：

- **RuoYi**：登录、验证码、管理员账号、菜单权限、操作日志——全部原生。新增仅 `com.ruoyi.caret` 一个包（3 个 Controller + 1 个 HTTP 客户端）。
- **codexhub**：新增内部 admin API。`HUB_ADMIN_ADDR`（默认空 = 关闭；部署设 `127.0.0.1:8768`）单独 `http.Server` 监听，nginx 不代理 —— 公网不可达是第一道墙，Bearer 密钥是第二道。
- **数据所有权不变**：卡密/名册仍在 hub 的 SQLite；实时状态仍在 hub 内存；Java 层无状态纯转发；明文卡密不落任何持久层。

## 4. hub 内部 admin API（Go）

认证：`Authorization: Bearer $HUB_ADMIN_KEY`，SHA-256 + `crypto/subtle` 常数时间比较（与现有 token 纪律一致）。密钥为空时整个监听不启动。

| 端点 | 请求 | 响应 | 后端调用 |
|---|---|---|---|
| `POST /admin/keys` | `{plan, machineLimit, months}` | `{key}` 明文**仅此一次** | `IssueKey`（现有） |
| `GET /admin/keys` | — | `[{prefix, plan, limit, used, expiresAt, revoked}]` | `ListKeys`（现有） |
| `POST /admin/keys/revoke` | `{prefix}` | `{}` | `RevokeKeyByPrefix`（**新增**） |
| `POST /admin/keys/extend` | `{prefix, months}` | `{}` | `ExtendKeyByPrefix`（**新增**） |
| `GET /admin/machines` | — | `[{machineId, name, keyPrefix, activatedAt, lastSeen, online, phoneSessions}]` | `ListMachines`（**新增**）+ hub 内存合并 |
| `POST /admin/machines/unbind` | `{machineId}` | `{}` | `AdminDeactivate`（现有）+ 踢下线（现有 kickAgent） |
| `GET /admin/status` | — | `{uptime, version, goVersion, memMB, agentsOnline, phoneSessions, agents:[{machineId, name, keyPrefix, connectedAt, phones}]}` | hub 内存 + runtime |

`internal/license` 新增（只读/单行 UPDATE，不动表结构）：

- `RevokeKeyByPrefix(prefix string) error`、`ExtendKeyByPrefix(prefix string, months int) error` —— prefix 命中多行时拒绝（`ErrPrefixAmbiguous`，提示用 CLI 全 key 操作）；CLI 的按全 key 路径保留不动。
- `ListMachines() ([]AdminMachineInfo, error)` —— machines × accounts × license_keys 三表 join，带 keyPrefix。

hub 的 `agent` 结构补 `connectedAt time.Time`（注册时间，一行赋值）。

错误映射：缺/错 Bearer→401；参数非法→400；prefix 不存在或机器未知→404；prefix 歧义→409。响应体统一 `{"error":"..."}`。ByPrefix 变体只改「定位方式」，吊销/续期语义与现有全 key 版本完全一致（如续期不限订阅状态，沿 `accountByKeyAnyState`）。

## 5. RuoYi 侧（Java 薄代理，`admin/ruoyi-ext`）

- 包 `com.ruoyi.caret`，源码目录直接拷入 `ruoyi-admin` 模块（drop-in，不改 RuoYi 其他文件）。
- `CaretKeyController`（`/caret/key`：list/issue/revoke/extend）、`CaretMachineController`（`/caret/machine`：list/unbind）、`CaretStatusController`（`/caret/status`）。每个方法挂 `@PreAuthorize("@ss.hasPermi('caret:key:list')")` 等权限串。
- `HubAdminClient`：RestTemplate，配置 `caret.hub.url` / `caret.hub.key`（`application-caret.yml` 样例入库，真实密钥只在服务器配置）。非 2xx 透传 hub 的 `error` 文案为 `AjaxResult.error(msg)`。
- 审计：吊销/续期/解绑挂 `@Log` 进 RuoYi 操作日志；**发卡接口关闭响应体记录**（明文卡密不得落 oper_log 表；若所用 RuoYi 版本的 `@Log` 不支持关闭响应记录，则该接口不挂 `@Log`，由 hub 端日志兜底）。
- `sql/caret_menu.sql`：目录「Caret 运营」+ 三个菜单（卡密管理/机器名册/中继状态）+ 按钮权限，插 `sys_menu`。
- 上游版本：RuoYi-Vue 官方 release，部署时锁定确切 tag 写入 `admin/ruoyi-ext/README.md`。

## 6. 前端（`admin/web`，模板增量）

- 模板原样保留（登录、布局、系统管理直连 RuoYi），`VITE_API_URL` 指向自建 RuoYi。
- 新增 `src/api/caret/{key,machine,status}/`（index.ts + types.ts，沿模板 api 模块惯例）与三个视图：
  - **卡密管理**：ProCrud 表格（前缀/套餐/已用/上限/到期/状态）；「生成卡密」弹窗（plan、机器数、月数）→ 成功后一次性明文展示 + 复制按钮 + “关闭后不再显示”提示；行内吊销（确认框）、续期（月数输入）。
  - **机器名册**：全量机器表（机器名、ID、所属卡密前缀、激活时间、最后在线、在线标记），行内解绑（确认框，提示会踢下线）。
  - **中继状态**：顶部统计卡片（在线机器/手机会话/hub 运行时长/内存）+ 在线机器明细表；10s 轮询。
- `.env` 中的 `VITE_LICENSE`（ele-admin-plus 付费授权码）**不入库**：gitignore `.env`，提交 `.env.example` 占位。
- 用不到的演示模块（AI 聊天、高德地图等）不删除，仅从菜单 SQL 不配置入口；体积裁剪后置。

## 7. 部署（香港中继，宝塔）

- 宝塔装 JDK17 + MySQL + Redis（均只绑 127.0.0.1）；RuoYi 打 jar，systemd 常驻 :8080。
- 子域名（占位 `admin.example.com`，上线前定）：nginx 站点 root 指向前端 dist，`/prod-api/` 反代 `127.0.0.1:8080/`；TLS 走宝塔证书。
- codexhub 服务加环境变量 `HUB_ADMIN_ADDR=127.0.0.1:8768`、`HUB_ADMIN_KEY=<openssl rand -hex 32>`；同一密钥写进 RuoYi 服务器端配置。
- 加固清单：RuoYi admin 默认密码必改；`/druid/**` 与 actuator 不对公网（nginx deny 或配置关闭）；验证码保持开启；MySQL/Redis 不开公网端口。

## 8. 错误处理与展示

- hub→Java：HTTP 状态码 + `{"error"}`（见 §4 映射）；hub 不可达时 Java 返回 `AjaxResult.error("中继服务不可达")`。
- Java→前端：RuoYi 标准 `{code, msg}`；前端按模板惯例 ElMessage 弹错，状态页轮询失败时显示「中继离线」横幅而非弹窗刷屏。

## 9. 测试

- **Go**（主要覆盖面）：`internal/license` 新函数的单测（prefix 命中/歧义/不存在；join 字段正确）；admin API 的 `httptest` 单测——401 门、各端点 happy path、错误映射、`HUB_ADMIN_KEY` 为空不监听。
- **Java**：薄代理不写单测；交付时跑一次「mock hub → Controller」集成冒烟（或手测清单代替）。
- **前端**：`vue-tsc --noEmit` + 手测清单（发卡→复制→激活真机→名册可见→解绑→踢下线生效）。
- 端到端验收：CLI 与 Web 各发一张卡，交叉在对方界面可见且操作互通。

## 10. 实施切分（供 writing-plans 展开）

1. Go：`internal/license` 三个新函数 + 测试 → hub admin API + 测试
2. Java：`admin/ruoyi-ext` 模块 + 菜单 SQL + 配置样例
3. 前端：`admin/web` 落库（gitignore 处理）→ api 模块 + 三页面
4. 部署：宝塔/手动装 RuoYi 全家桶 → nginx 站点 → 联调验收
