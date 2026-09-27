# cops — Copilot CLI BYOK 供应商一键切换代理

`cops`（**C**opilot **P**rovider **S**witch）是一个本地 OpenAI 兼容反向代理，
专为 [GitHub Copilot CLI](https://docs.github.com/copilot/how-tos/use-copilot-agents/use-copilot-cli) 的
BYOK（Bring Your Own Key）机制设计，实现类似 CCSwitch 的**模型供应商一键切换**体验。
支持**系统托盘常驻**：启动自动注入环境变量，退出自动恢复。

## 为什么用代理而不是改配置？

CCSwitch 类工具通过改写配置文件切换供应商，CLI 需要重启才能生效。cops 采用**代理中间层**方案：

```
Copilot CLI ──固定指向──▶ cops 代理(127.0.0.1:8317/v1) ──路由──▶ DeepSeek / GLM / OpenRouter / Ollama / ...
```

| 能力 | 说明 |
|---|---|
| ⚡ 即时切换 | `cops switch` 只改代理路由，base URL 不变，**无需重启 Copilot CLI** |
| 🔑 密钥不落 shell | 真实 API Key 只存在 `~/.cops/config.json`，环境变量里是占位符 |
| 🔀 模型改写 | 请求体中的 `model` 自动改写为激活供应商的模型，CLI 侧配置一次到位 |
| 📊 用量统计 | 按供应商/模型/日聚合 token 与估算费用（含 SSE 流式 usage 捕获） |
| 📝 请求日志 | JSONL 明细 + 大小轮转 + 保留天数，`cops logs` / Web 页可查 |
| 🖥️ Web 管理页 | 内置于代理（`/_cops/`），供应商增删改查、一键启用 |

## 安装

前置要求：[Go 1.25+](https://go.dev/dl/)（Windows/macOS/Linux 均可构建）。

```powershell
git clone <本仓库> D:\personal\repos\copilot-byok-switch
cd D:\personal\repos\copilot-byok-switch
.\build.bat          # 产出 cops.exe（单文件、无运行时依赖）
```

## 快速上手

```powershell
# 1. 添加供应商（OpenAI 兼容上游）
cops add glm --url https://open.bigmodel.cn/api/paas/v4 --key <你的Key> --model glm-4.7
cops add deepseek --url https://api.deepseek.com/v1 --key <你的Key> --model deepseek-chat
cops add ollama --url http://localhost:11434/v1 --model qwen3:32b

# 2. 一键安装：注入 COPILOT_* 用户环境变量（带备份）+ 托盘开机自启
cops install

# 3. 启动托盘（本机即刻生效；开机自启已设置）
cops tray

# 4. 打开新的终端（必须新开！），运行 copilot 即走 glm
# 5. 随时切换 —— 托盘菜单点一下，或命令行，均无需重启 copilot
cops switch deepseek
cops switch glm --model glm-4-flash

# 诊断 / 管理
cops doctor        # 体检：配置、守护进程、注入状态、自启、上游连通
cops open          # 浏览器打开 Web 管理页
cops stats         # 用量与费用
cops logs -f       # 跟随请求日志
cops uninstall     # 恢复环境变量 + 清理自启（配置保留）
```

## 托盘模式（推荐常驻方式）

`cops tray` = 托盘 UI + 代理守护进程，单进程：

- **启动即注入**：自动写入 4 个 `COPILOT_*` 用户环境变量指向本地代理；
  注入前原值备份到 `~/.cops/env-backup.json`（二次启动不覆盖最早备份）
- **退出即恢复**：托盘菜单「退出（恢复环境变量并停止代理）」—— 原值还原、代理关停，"停止即还原"
- **托盘菜单**：供应商列表一键切换（勾选=当前）、打开管理页、停用/启用注入
- **附着模式**：若检测到已有守护进程（如 `cops serve`）在运行，托盘仅作 UI；
  退出时通过管理 API 停掉该守护进程，语义不变
- **崩溃自愈**：托盘被强杀后注入状态保留（备份文件在），`cops restore` 或下次启动可正确恢复
- 菜单每 2 秒轮询刷新，Web 管理页 / `cops switch` 的变更会自动同步到菜单
- `--console` 参数保留控制台便于调试；开机自启以最小化方式启动

注意：环境变量注入/恢复**只影响新开的终端**；已运行的 copilot 会话在托盘退出后会断流（代理已停），重开终端即走官方认证。

## 命令一览

| 命令 | 说明 |
|---|---|
| `cops tray [--console]` | 托盘常驻（推荐）：启动注入 / 退出恢复 / 菜单切换 |
| `cops serve [--headless]` | 前台守护进程（无托盘） |
| `cops install [--model X]` | 注入 4 个 `COPILOT_*`（带备份）+ HKCU Run 自启（cops tray） |
| `cops uninstall` | 恢复环境变量 + 清自启（保留 `~/.cops/` 数据） |
| `cops inject` / `cops restore` | 手动注入 / 恢复环境变量（托盘崩溃后兜底） |
| `cops add [名称] --url --key --model [--models] [--price-in --price-out]` | 添加供应商 |
| `cops list` / `cops current` | 列出供应商 / 查看当前激活 |
| `cops switch <名称> [--model X]` | 一键切换（守护进程运行中时即时生效） |
| `cops remove <名称>` | 删除供应商 |
| `cops test [名称]` | 上游连通性测试 |
| `cops stats [--days N]` | 用量费用统计 |
| `cops logs [-f] [--limit N]` | 请求日志 |
| `cops open` | 打开 Web 管理页 |
| `cops doctor` | 全面体检（含注入状态） |

## 工作原理

1. `cops install` 写入用户环境变量（HKCU\Environment，并广播 WM_SETTINGCHANGE）：
   - `COPILOT_PROVIDER_TYPE=openai`
   - `COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:8317/v1`（固定指向本地代理）
   - `COPILOT_PROVIDER_API_KEY=cops-local`（占位符，真实 Key 由代理注入）
   - `COPILOT_MODEL=cops-active`（虚拟模型名，由代理改写为真实模型）
2. Copilot CLI（BYOK 模式，OpenAI wire）将请求发往 `/v1/chat/completions` 等端点；
3. 代理按当前激活供应商：
   - 剥离路径 `/v1` 前缀后拼接到供应商 BaseURL（OpenAI SDK 约定）；
   - 注入真实 `Authorization: Bearer <Key>` 与自定义头；
   - 将请求体 `model` 改写为该供应商的模型（可用 `passthroughModel` 关闭）；
   - 流式请求自动注入 `stream_options.include_usage` 以捕获用量；
   - SSE 逐块透传并即时刷新，同时扫描末尾 usage chunk；
4. `cops switch` 通过管理 API 热更新内存配置 —— 代理端点不变，CLI 无感知。

### 配置文件 `~/.cops/config.json`

```jsonc
{
  "listen": "127.0.0.1:8317",
  "active": "glm",
  "virtualModel": "cops-active",
  "providers": [{
    "name": "glm",
    "baseUrl": "https://open.bigmodel.cn/api/paas/v4",  // OpenAI SDK 约定含 /v1
    "apiKey": "sk-...",
    "model": "glm-4.7",
    "models": ["glm-4.7", "glm-4-flash"],
    "prices": { "glm-4.7": { "inputPerMillionTokens": 0.5, "outputPerMillionTokens": 2 } },
    "timeoutSec": 600,
    "passthroughModel": false,   // true=不改写 model
    "noUsageInjection": false    // true=不注入 include_usage（上游不兼容时用）
  }],
  "requestLog": { "enabled": true, "maxBodyKB": 32, "retainDays": 7, "maxFileMB": 20 }
}
```

常用上游 BaseURL 参考：

| 供应商 | BaseURL |
|---|---|
| OpenAI | `https://api.openai.com/v1` |
| DeepSeek | `https://api.deepseek.com/v1` |
| 智谱 GLM | `https://open.bigmodel.cn/api/paas/v4` |
| OpenRouter | `https://openrouter.ai/api/v1` |
| SiliconFlow | `https://api.siliconflow.cn/v1` |
| Ollama | `http://localhost:11434/v1` |
| LM Studio | `http://localhost:1234/v1` |

## 常见问题

**Q: 切换供应商后 Copilot CLI 报错模型不存在？**
代理默认把 `model` 改写为激活供应商的 `model` 字段；确认该供应商已配置正确的模型名（`cops current` 查看）。

**Q: `cops install` 后 copilot 还是走官方认证？**
已开着的终端读不到新环境变量 —— 必须新开终端。仍不行时运行 `cops doctor` 逐项排查。

**Q: 流式请求的用量是 0？**
部分上游不支持 `stream_options.include_usage`，此时无法拿到准确 token 数（日志会如实记录）。可为该供应商设置 `noUsageInjection: true` 避免兼容性问题。

**Q: `/model` 显示的是 cops-active，上下文窗口信息不准？**
虚拟模型名由代理改写，CLI 侧能力探测可能不准。追求精确显示可改用 `passthroughModel: true` 并把 `COPILOT_MODEL` 设为真实模型名。

**Q: 安全性？**
代理仅监听 `127.0.0.1`，管理页无鉴权（本机场景）。API Key 明文存于 `~/.cops/config.json`，请勿把该目录加入同步/备份到不受信位置。若改 `listen` 为 `0.0.0.0` 需自行承担风险。

**Q: 如何恢复官方 Copilot？**
托盘菜单「退出」（自动恢复环境变量并停止代理），或 `cops uninstall`，然后新开终端即可。

## 开发

```powershell
go test ./...       # 单元测试（含 httptest 假上游的代理全链路）
go vet ./...
go run . serve      # 源码直接运行
```

路线图（v0.2 多模型路由：虚拟模型 pro/flash 分工、按请求规模自动路由）见 [ROADMAP.md](ROADMAP.md)。

- `devtools/mockupstream`：模拟 OpenAI 兼容上游，用于手工冒烟测试
  （`go run ./devtools/mockupstream 9911 mock-a`）
- `devtools/icongen`：重新生成托盘图标（`go run ./devtools/icongen` → `internal/tray/icon.ico`）
- 目录结构：`internal/proxy`（核心反代）、`internal/tray`（托盘 UI）、`internal/admin`（管理 API+Web）、
  `internal/config`（配置）、`internal/stats`（用量）、`internal/reqlog`（日志）、
  `internal/cli`（命令）、`internal/winenv`（Windows 集成：环境变量注入/恢复、注册表）

## 许可

私有项目，未设开源许可。
