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
| 🧭 多模型路由 | 虚拟模型表 + utility 识别：重活走 pro、辅助调用走 flash；`cops model use` / 托盘 / Web 管理页一键切默认，无需重启会话 |
| 📏 上下文窗口 | 真实模型级/虚拟模型级两级配置（`128k` 简写），`/v1/models` 输出 `context_length` 供其他 OpenAI 客户端读取 |
| 📊 用量统计 | 按供应商/模型/日聚合 token 与估算费用（含 SSE 流式 usage 捕获） |
| 🚦 限额用量 | 订阅套餐（GLM Coding Plan 优先）多窗口用量查询：5小时/周/月各自占比与重置时间，`cops quota` 一览 |
| 🔄 限额自动切换 | 任一窗口用量达到阈值（默认 95%）自动切到候选顺序中首个未超阈值的供应商，事件留痕 |
| 📝 请求日志 | JSONL 明细 + 大小轮转 + 保留天数，`cops logs` / Web 页可查 |
| 🖥️ Web 管理页 | 内置于代理（`/_cops/`）：一键切换供应商/模型、路由可视化编辑（即时保存）、用量图表、限额用量条与自动切换设置、请求日志 |

## 安装

前置要求：[Go 1.26+](https://go.dev/dl/)（Windows/macOS/Linux 均可构建）。

```powershell
git clone <本仓库> D:\personal\repos\copilot-byok-switch
cd D:\personal\repos\copilot-byok-switch
.\build.bat          # 产出 cops.exe（单文件、无运行时依赖）
```

也可从 GitHub Actions 构建产物获取预编译二进制：master 分支最新 artifact（Windows `cops-windows-x64.zip`；macOS `cops-macos-universal.tar.gz` / Finder 应用 `cops-macos-universal-app.zip`），或打 `v*` 标签后的 Release。

## 快速上手

```powershell
# 1. 添加供应商（OpenAI 兼容上游）
cops add glm --url https://open.bigmodel.cn/api/paas/v4 --key <你的Key> --model glm-4.7
cops add deepseek --url https://api.deepseek.com/v1 --key <你的Key> --model deepseek-chat
cops add ollama --url http://localhost:11434/v1 --model qwen3:32b

# 2. 一键安装：注入 COPILOT_* 用户环境变量（带备份）+ 托盘开机自启
cops install

# 3. 开机自启已设置；macOS 上 install 会立即加载托盘。
#    Windows 首次安装后手动运行 cops tray；也可双击 cops.exe

# 4. 打开新的终端（必须新开！），运行 copilot 即走 glm
# 5. 随时切换 —— 托盘菜单点一下，或命令行，均无需重启 copilot
cops switch deepseek
cops switch glm --model glm-4-flash

# 6. 多模型分工（可选）：虚拟模型路由，同一供应商也能 pro/flash 分身
cops model add cops-pro --provider glm --model glm-4.7
cops model add cops-flash --provider glm --model glm-4-flash
cops model use cops-pro      # 切默认虚拟模型，即时生效（环境变量不动）

# 诊断 / 管理
cops doctor        # 体检：配置、守护进程、注入状态、自启、上游连通
cops open          # 浏览器打开 Web 管理页
cops stats         # 用量与费用
cops quota         # 限额用量（GLM Coding Plan 等订阅套餐：5小时/周/月窗口占比）
cops autoswitch    # 查看限额自动切换配置与最近事件
cops logs -f       # 跟随请求日志
cops uninstall     # 恢复环境变量 + 清理自启（配置保留）
```

### 限额用量与自动切换（v1.2）

订阅制套餐（如 GLM Coding Pro/Max）通常有**多重限额窗口**（5 小时 + 周/月），
额度耗尽前主动切换供应商比被动等 429 体验好得多。cops 内置用量查询与阈值自动切换：

```powershell
# 1. 添加 GLM Coding Plan 供应商（BaseURL 域名命中 bigmodel.cn / z.ai 即自动启用用量查询）
cops add glm --url https://open.bigmodel.cn/api/coding/paas/v4 --key <套餐Key> --model glm-4.7
cops add glm2 --url https://open.bigmodel.cn/api/coding/paas/v4 --key <另一个套餐Key> --model glm-4.7
cops add deepseek --url https://api.deepseek.com/v1 --key <Key> --model deepseek-chat   # 按量付费，无限额窗口

# 2. 查看各窗口用量（5小时 / 周 / 月占比 + 重置时间）
cops quota

# 3. 启用自动切换：任一窗口占比 ≥ 阈值时，按候选顺序切到第一个未超阈值的供应商
cops autoswitch on
cops autoswitch set --threshold 95 --interval 300          # 达 95% 切换，每 5 分钟轮询
cops autoswitch set --order glm2,deepseek                  # 候选优先顺序（缺省按配置顺序）
cops autoswitch                                          # 查看配置与最近切换事件
```

- **判定口径**：每个供应商可能有多个限额窗口，取**占比最高的窗口**（约束最紧的一个）与阈值比较；
  未配置用量查询的供应商视为"用量未知"，可作为切换目标（如按量付费 DeepSeek）。
- **查询端点**：GLM 使用智谱控制台内部接口 `GET /api/monitor/usage/quota/limit`
  （官方 Claude Code 插件同款；国内 `open.bigmodel.cn`、国际 `api.z.ai` 自动适配；
  团队版在 `quota.organizationId` / `quota.projectId` 补齐标识）。
- 自动切换引擎随守护进程（`cops serve` / `cops tray`）运行，切换走既有热切换通道，**无需重启 Copilot CLI**；
  全部候选超阈值时保持不变并记录事件（不刷屏去重）；Web 管理页总览有用量条与可视化配置。

#### 团队版（组织/项目）查询范围

智谱的用量端点**默认始终查询个人范围**，不理会 API Key 所属的项目——同一个账号下
"团队套餐 + 个人套餐"两个供应商若都不绑定团队标识，会查出**完全相同的个人用量**
（cops 会明确警告"与 xx 查到完全相同的用量"，自动切换也会跳过这类同套餐候选）。
团队供应商需绑定查询范围：

```powershell
cops quota team glm-work --org <组织ID> --project <项目ID>
cops quota unbind glm-work        # 解除，回到个人范围
```

ID 获取方式（智谱未提供公开查询 API，需从控制台取一次）：

1. 浏览器登录 bigmodel.cn，打开团队用量页 `bigmodel.cn/coding-plan/team/usage-stats`；
2. F12 → Network，刷新页面，找 `api/monitor/usage/quota/limit` 请求；
3. 复制请求头 `Bigmodel-Organization`（组织 ID）与 `Bigmodel-Project`（项目 ID）的值
   （也可在页面 localStorage 中找这两个字段；两个 ID 必须成对，缺一个查询会落回个人范围）。

也可在 Web 管理页供应商弹窗「团队查询范围」填写；`cops quota` 表格的"范围"列显示
个人/团队，绑定后用量应与个人套餐不同。

## 托盘模式（推荐常驻方式）

`cops tray` = 托盘 UI + 代理守护进程，单进程。**裸 `cops`（无参数）、Windows 双击 `cops.exe`、macOS 双击 `Cops.app` 均启动托盘模式**（Windows 双击检测到控制台仅本进程时自动重启为无控制台的分离进程，黑窗一闪即关、不留残留；在终端中运行则保持原行为，`cops --console` 可保留控制台调试）：

- **启动即注入**：自动写入 4 个 `COPILOT_*` 用户环境变量指向本地代理；
  注入前原值备份到 `~/.cops/env-backup.json`（二次启动不覆盖最早备份）
- **退出即恢复**：托盘菜单「退出（恢复环境变量并停止代理）」—— 原值还原、代理关停，"停止即还原"
- **托盘菜单**：模型（虚拟名单选，v0.2）、供应商一键切换（勾选=当前）、打开管理页、停用/启用注入
- **附着模式**：若检测到已有守护进程（如 `cops serve`）在运行，托盘仅作 UI；
  退出时通过管理 API 停掉该守护进程，语义不变
- **崩溃自愈**：托盘被强杀后注入状态保留（备份文件在），`cops restore` 或下次启动可正确恢复
- 菜单每 2 秒轮询刷新，Web 管理页 / `cops switch` 的变更会自动同步到菜单
- `--console` 参数保留控制台便于调试；开机自启以最小化方式启动

注意：环境变量注入/恢复**只影响新开的终端**；已运行的 copilot 会话在托盘退出后会断流（代理已停），重开终端即走官方认证。

## macOS

macOS 支持完整 CLI、Web 管理页、原生菜单栏托盘、用户级 LaunchAgent 开机自启，以及 Intel/Apple Silicon universal binary。获取发布包后：

```sh
# 命令行安装：把二进制放在稳定路径，LaunchAgent 会使用该路径启动
tar -xzf cops-macos-universal.tar.gz
mkdir -p "$HOME/.local/bin"
mv cops "$HOME/.local/bin/cops"
"$HOME/.local/bin/cops" install
```

也可解压 `cops-macos-universal-app.zip`，将 `Cops.app` 移入 `/Applications` 后双击启动；应用以菜单栏常驻，不在 Dock 显示。首次启动后可用菜单栏图标管理供应商、模型和注入状态。`cops install` 会安装并加载 `~/Library/LaunchAgents/com.cops.tray.plist`，因此托盘会立即启动，并在之后登录时自动启动；不要再重复运行 `cops tray`。

- **环境变量**：cops 在 `~/.zshrc` 中维护 `# >>> cops >>>` 标记块；`cops install` / `cops inject` 注入，退出托盘、`cops restore` 或 `cops uninstall` 恢复。注入时会保留已有 `NO_PROXY`/`no_proxy` 规则，并添加 `localhost`、`127.0.0.1`、`::1`，避免 Copilot 访问本机代理时被外部 HTTP 代理拦截。仅对新启动的 zsh 终端生效。
- **构建**：本机构建需 Xcode Command Line Tools（`xcode-select --install`）和 Go 1.26+；GitHub Actions 发布 arm64 + amd64 universal binary 与 `.app`。
- **首次运行**：发布包未经过 Apple notarization；若 Gatekeeper 阻止打开，请在系统设置「隐私与安全性」中允许，或在 Finder 中右键应用并选择「打开」。

验证注入可在新终端运行 `env | grep COPILOT`。卸载自启动并恢复原环境变量：`cops uninstall`。

## 命令一览

| 命令 | 说明 |
|---|---|
| `cops` / `cops.exe 双击` / macOS `Cops.app` 双击 | 无参数默认进托盘（= `cops tray`，启动守护进程） |
| `cops tray [--console]` | 托盘常驻（推荐）：启动注入 / 退出恢复 / 菜单切换 |
| `cops serve [--headless]` | 前台守护进程（无托盘） |
| `cops install [--model X]` | 注入 4 个 `COPILOT_*`（带备份）+ 设置平台用户级自启（cops tray） |
| `cops uninstall` | 恢复环境变量 + 清理自启（保留 `~/.cops/` 数据） |
| `cops inject` / `cops restore` | 手动注入 / 恢复环境变量（托盘崩溃后兜底） |
| `cops add [名称] --url --key --model [--models] [--price-in --price-out] [--context 128k] [--max-prompt-tokens 128k]` | 添加供应商（--context 为默认模型上下文；--max-prompt-tokens 覆盖注入值，留空自动推导） |
| `cops list` / `cops current` | 列出供应商 / 查看当前激活 |
| `cops switch <名称> [--model X]` | 一键切换（守护进程运行中时即时生效） |
| `cops model list` | 列出虚拟模型路由与 utility 配置 |
| `cops model add <虚拟名> --provider <供应商> --model <真实模型> [--context 128k]` | 添加虚拟模型（第一个自动成为默认；--context 覆盖上下文，留空回退真实模型值） |
| `cops model remove <虚拟名>` | 删除虚拟模型（默认自动回退） |
| `cops model use <虚拟名>` | 切换默认虚拟模型（即时生效，无需重启会话） |
| `cops remove <名称>` | 删除供应商 |
| `cops test [名称]` | 上游连通性测试 |
| `cops stats [--days N]` | 用量费用统计 |
| `cops quota [名称]` | 限额用量查询（多窗口占比、重置时间与查询范围；相同用量自动告警） |
| `cops quota team <名称> --org <ID> --project <ID>` | 绑定团队（组织/项目）查询范围（GLM 团队版必需） |
| `cops quota unbind <名称>` | 解除团队范围，回到个人范围查询 |
| `cops autoswitch` | 查看限额自动切换配置、最近检查与切换事件 |
| `cops autoswitch on/off` | 启用 / 停用限额自动切换 |
| `cops autoswitch set --threshold 95 --interval 300 --order a,b` | 修改阈值（百分比）/ 轮询间隔（秒）/ 候选顺序 |
| `cops logs [-f] [--limit N]` | 请求日志 |
| `cops open` | 打开 Web 管理页 |
| `cops doctor` | 全面体检（含注入状态） |

## 工作原理

1. `cops install` 写入用户环境变量（Windows 使用 HKCU\Environment 并广播 WM_SETTINGCHANGE；macOS 使用 `~/.zshrc` 标记块）：
   - `COPILOT_PROVIDER_TYPE=openai`
   - `COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:8317/v1`（固定指向本地代理）
   - `COPILOT_PROVIDER_API_KEY=cops-local`（占位符，真实 Key 由代理注入）
   - `COPILOT_MODEL=cops-active`（虚拟模型名，由代理改写为真实模型）
   - `COPILOT_PROVIDER_MAX_PROMPT_TOKENS=128000`（条件注入：按主对话目标自动推导，全未知则不写）
2. Copilot CLI（BYOK 模式，OpenAI wire）将请求发往 `/v1/chat/completions` 等端点；
3. 代理按路由目标转发：
   - 剥离路径 `/v1` 前缀后拼接到**目标供应商** BaseURL（OpenAI SDK 约定）；
   - 注入真实 `Authorization: Bearer <Key>` 与自定义头；
   - 按四级路由解析请求体 `model`（见下节「模型路由」）；
   - 流式请求自动注入 `stream_options.include_usage` 以捕获用量；
   - SSE 逐块透传并即时刷新，同时扫描末尾 usage chunk；
4. `cops switch` / `cops model use` 通过管理 API 热更新内存配置 —— 代理端点不变，CLI 无感知。

### 模型路由（v0.2）

个人 BYOK 下 Copilot CLI 的 `/model` 不可用（会话被钉死在单一 `COPILOT_MODEL`），
cops 因此在代理侧做**确定性多模型路由**，按请求体 `model` 字段四级解析：

| 优先级 | 规则 | 命中条件与去向 |
|---|---|---|
| 1 | `utility` | model 命中 `utilityPatterns`（glob，内置默认 `*nano* *-mini* *fast*`）→ utility 目标。CLI 内部辅助调用（摘要/标题）会发 `gpt-5.4-nano` 这类内部 id，正好路由到便宜快速模型 |
| 2 | `virtual` | model 命中虚拟模型表（可跨供应商）→ 表内目标 |
| 3 | `pinned` | model == 钉住名（`COPILOT_MODEL`）且已设默认虚拟模型 → `defaultVirtual` 目标 |
| 4 | `default` | 兜底 → 激活供应商默认模型（`passthroughModel: true` 时透传原 model） |

- 未知 model id 一律走兜底，**永不向上游 404**（规避 CLI 内部 id 直发上游的问题）；
- 命中 1-3 时，转发参数（BaseURL/Key/超时/自定义头/`stripSampling`）取**目标供应商**；
- 切默认模型三入口等价：`cops model use`、托盘「模型」菜单、Web 管理页 —— 都只改代理侧映射，环境变量不动，即时生效；
- `COPILOT_PROVIDER_MAX_PROMPT_TOKENS`（上下文上限，驱动 Copilot CLI 自动压缩阈值 ≈95%）由 cops 自动维护：取主对话目标（默认虚拟模型 > 激活供应商默认模型）的供应商级 `maxPromptTokens` 覆盖 > 虚拟 `contextWindow` > 真实模型 `modelContext`，全未知则不注入（沿用你手动设置的值）。切换供应商/模型/路由后 cops 即刻刷新该变量；但 Copilot CLI **在进程启动时读取一次**，上下文差异大的切换后建议新开 CLI 会话；
- 请求日志记录 `virtualModel`（原始名）与 `routeRule`（命中的规则），用量统计仍按真实模型聚合。

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
    "modelContext": { "glm-4.7": 128000, "glm-4-flash": 8000 },  // 真实模型上下文（token），/v1/models 的 context_length 来源
    "maxPromptTokens": 200000,  // 可选：覆盖注入的 COPILOT_PROVIDER_MAX_PROMPT_TOKENS；0/缺省 = 按 主对话目标自动推导
    "prices": { "glm-4.7": { "inputPerMillionTokens": 0.5, "outputPerMillionTokens": 2 } },
    "timeoutSec": 600,
    "passthroughModel": false,   // true=不改写 model
    "noUsageInjection": false,   // true=不注入 include_usage（上游不兼容时用）
    "stripSampling": false,      // true=剥离 temperature/top_p 等采样参数（CLI 会强制发 temperature:0，思考模型上游建议开启）
    "quota": {                   // 限额用量查询（v1.2；域名命中 bigmodel.cn/z.ai 可省略整段）
      "kind": "glm",             // ""=按 BaseURL 自动探测 / "glm" / "none"=关闭
      "apiKey": "",              // 查询密钥覆盖；空=复用供应商 apiKey
      "baseUrl": "",             // 查询根地址覆盖；空=取 BaseURL 的 scheme://host
      "organizationId": "",      // 团队版标识（个人版留空）
      "projectId": ""
    }
  }],
  "autoSwitch": {                // 限额自动切换（v1.2）
    "enabled": true,
    "thresholdPct": 95,          // 任一窗口占比达到即切换（缺省 95）
    "intervalSec": 300,          // 轮询间隔（缺省 300，最小 30）
    "order": ["glm2", "deepseek"] // 候选优先顺序；缺省=providers 顺序
  },
  "routing": {                                // v0.2 模型路由（可选；缺省时全部走激活供应商默认模型，等价 v0.1）
    "virtualModels": {
      "cops-pro":   { "provider": "glm", "model": "glm-4.7" },
      "cops-flash": { "provider": "glm", "model": "glm-4-flash", "contextWindow": 32000 }  // 可选覆盖；缺省回退真实模型 modelContext 值
    },
    "defaultVirtual": "cops-pro",             // 钉住名当前指向；cops model use 改这里
    "utility":        { "provider": "glm", "model": "glm-4-flash" },
    "utilityPatterns": ["*nano*", "*-mini*"]  // 留空用内置默认 *nano* *-mini* *fast*
  },
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

**Q: 想让重活走 pro、辅助小任务走 flash 怎么配？**
`cops model add cops-pro --provider glm --model glm-4.7`、`cops model add cops-flash --provider glm --model glm-4-flash` 后 `cops model use cops-pro`。CLI 内部辅助调用（摘要/标题等）可在 Web 管理页或配置里加 utility 路由到 flash。托盘「模型」菜单随时一键切换。

**Q: 限额用量查询报错 / 没有数据？**
用量查询使用套餐侧端点，需满足：① 该供应商是订阅套餐（GLM 需为 Coding Plan 套餐 key，按量付费 key 会报"不存在 coding plan"）；② `quota.kind` 已启用（bigmodel.cn / z.ai 域名自动启用，其他域名需 `cops add --quota glm` 或 Web 弹窗选择）；③ 团队版需补齐 `quota.organizationId` / `quota.projectId`（`cops quota team` 绑定，见上节）。自动切换不依赖用量查询的供应商仍可作为候选（视为用量未知）。

**Q: 两个供应商查出的用量完全一样？**
智谱用量端点默认查询**个人范围**、不理会 Key 的项目归属。团队套餐供应商必须绑定团队查询范围（`cops quota team <名称> --org --project`），否则查到的是个人套餐的用量。cops 检测到完全相同的用量会在 `cops quota` 与 Web 管理页给出警告，自动切换也会跳过这类"同套餐"候选。

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

路线图（v0.2.1 autoRoute 按请求规模自动路由等）见 [ROADMAP.md](ROADMAP.md)。

- `devtools/mockupstream`：模拟 OpenAI 兼容上游，用于手工冒烟测试
  （`go run ./devtools/mockupstream 9911 mock-a`）
- `devtools/icongen`：重新生成托盘图标（`go run ./devtools/icongen` → `internal/tray/icon.ico`）
- 目录结构：`internal/proxy`（核心反代）、`internal/tray`（托盘 UI）、`internal/admin`（管理 API+Web）、
  `internal/config`（配置）、`internal/stats`（用量）、`internal/reqlog`（日志）、
  `internal/quota`（限额用量查询 + 自动切换决策）、`internal/cli`（命令）、
  `internal/winenv`（Windows 集成：环境变量注入/恢复、注册表）

## 许可

[MIT](LICENSE)
