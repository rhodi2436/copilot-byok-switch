# cops 迭代计划（ROADMAP）

> 已交付：**v0.1.0** 单激活供应商代理、一键热切换、install/doctor、Web 管理页、用量统计与请求日志；
> **v0.3 托盘常驻**（`cops tray`：启动自动注入环境变量、退出自动恢复并停止代理、托盘菜单快捷切换、附着/嵌入双模式、inject/restore 兜底命令）；
> **v0.2.0 代理侧确定性多模型路由**（四级解析：utility 识别 + 虚拟模型表 + 钉住名映射 + 默认兜底；`cops model` 命令族、托盘「模型」菜单、Web 管理页路由编辑区、stripSampling、doctor 路由校验；autoRoute 未实现，见 v0.2.1）；
> **v0.2.x 上下文窗口元数据**（两级配置：`providers[].modelContext` 真实模型级 + 虚拟模型 `contextWindow` 覆盖（0=回退）；CLI/Web 支持 `128k`/`1.5m` 简写；`/v1/models` 条目输出 `context_length`（OpenRouter 惯例）供其他 OpenAI 客户端读取；亦为 v0.2.1 autoRoute 按请求规模路由备好数据基础）；
> **v0.3.x 裸 cops 直启**（无参数 `cops` / 双击 `cops.exe` = `cops tray`：守护进程 + 托盘 + 启动即注入，已运行守护进程时自动附着；`cops --console` 保留控制台；所有子命令行为不变；双击/独占控制台启动时自我重启为无控制台分离进程，原进程立即退出，控制台窗口干净关闭——规避 Windows Terminal 下 `FreeConsole` 残留 `[process exited]` 黑窗的问题）；
> **v0.4 macOS 支持**（`winenv_darwin.go`：环境变量经 `~/.zshrc` 标记块注入/恢复，LaunchAgent 登录自启，原生 systray；Finder 可双击 `Cops.app` 启动菜单栏常驻；GitHub Actions 构建 arm64 + amd64 universal binary 与 `.app` 并发布）；
> **v0.3.x WebUI 修补与模型最大输出**（标签切换即时渲染 providers/routing；上下文输入统一「数值 × 单位」；新增 `providers[].modelMaxOutput` 真实模型级最大输出 token：请求中 `max_tokens`/`max_completion_tokens` 超限即被钳制（Copilot CLI 每请求固定发送 `max_tokens`，超出上游上限时部分供应商直接 400）、`/v1/models` 条目输出 `max_output_tokens`、Web 弹窗逐模型「上下文 / 最大输出」两组输入与卡片展示）。

## macOS 实机验收

- 菜单栏图标、菜单刷新、退出恢复环境变量并停止代理
- `Cops.app` Finder 双击启动，Dock 中不显示应用图标
- LaunchAgent 安装、登录启动、卸载及重复安装
- 注入/恢复往返、serve + Copilot CLI 穿透、切换供应商、Web 管理页

---

## 迭代 v0.3.x —— 待办小项

（已清空：双击直启已实现，见上方摘要。）

---

## 迭代 v0.2 —— 代理侧多模型路由（pro / flash 分工）

### 背景与动机

v0.1 的路由粒度是**供应商**：所有请求统一改写到当前激活供应商的单一 `model`。
典型诉求是"重活走 pro、快任务走 flash"，一次配置后按请求自动分发，而不是每次手动 `cops switch`。

### 调研结论（已核实）：个人 BYOK 下 Agent 侧无法选模型

> 针对 github/copilot-cli 公开 issue 与官方文档核实，原方案 A"CLI 侧 `/model` 配置"的前提**不成立**，路由决策全部收敛到 cops 代理层。

- **`/model` 在个人 BYOK 下不可用**：CLI 1.0.81+ 在 BYOK 环境变量模式下 `/model` 直接报 "Unknown command"（[#4672](https://github.com/github/copilot-cli/issues/4672)，1.0.80 尚可用，未修复）；
- **选择器只列 GitHub 托管模型**：即使可用，BYOK 会话也被钉死在单一 `COPILOT_MODEL`，无法在会话内切换自定义/本地模型（[#3709](https://github.com/github/copilot-cli/issues/3709)，open）；
- **CLI 不拉取 `/v1/models`**：从 provider 的 `/models` 端点填充选择器仍是未实现的功能请求（[#4358](https://github.com/github/copilot-cli/issues/4358)，open）——原方案"合成列表供 `/model` 选择"的假设不成立；
- **官方 BYOK 文档**只定义单一 `COPILOT_MODEL`；选择器集成仅存在于**企业托管** BYOK（管理员下发模型列表），与个人 env var 路径不同。

**关键新发现（[#4950](https://github.com/github/copilot-cli/issues/4950)）：请求体 `model` 字段本身就是可路由信号**

- CLI 1.0.81+ 对压缩（compaction）/ auto-mode 等**内部辅助调用**，会把内部模型 id（如 `gpt-5.4-nano`）直接发给 BYOK 端点（真实供应商上 404）；
- 即 `model` 字段并非恒等于 `COPILOT_MODEL`：内部辅助调用可被代理**确定性识别**，天然适合"轻活走 flash"；
- 同 issue 证实 1.0.81+ 强制发送 `temperature:0 / top_p:0.95` 等采样参数，对思考模型有害——代理可选择剥离。

---

### 修订方案：代理侧四级路由（确定性优先，启发式可选叠加）

```
1. 内部辅助调用   model 命中 utility 模式（如 *nano*）   → utility 目标（flash）
2. 显式映射       model 命中虚拟模型表                   → 对应 (供应商, 真实模型)
3. autoRoute      （可选，默认关）按输入 token 估算       → small / large 目标
4. 默认兜底       未命中 / 未知 id（含未识别的内部 id）   → 激活供应商默认
```

- **手动切换模型**不再依赖 `/model`，由 cops 自己提供入口：`cops model use <虚拟名>`、托盘菜单、Web 管理页——修改的是"钉住名 → 真实目标"映射，后续请求立即生效，无需重启会话、无需重开终端（环境变量不动）；
- 未识别的 model id 一律走兜底，**永不向上游 404**（顺带规避 #4950 类问题）；
- `/v1/models` 合成列表保留：Copilot CLI 不消费（#4358），但对其他 OpenAI 客户端与调试有用。

配置示意：

```jsonc
"routing": {
  "virtualModels": {                        // 显式映射（虚拟名 → 目标），可跨供应商
    "cops-pro":   { "provider": "glm",    "model": "glm-4.7" },
    "cops-flash": { "provider": "glm",    "model": "glm-4-flash" },
    "cops-local": { "provider": "ollama", "model": "qwen3:32b" }
  },
  "defaultVirtual": "cops-pro",             // 钉住名当前指向；cops model use 改这里
  "utility":        { "provider": "glm", "model": "glm-4-flash" },
  "utilityPatterns": ["*nano*", "*-mini"],  // 内部辅助调用识别（glob）
  "autoRoute": {                            // 可选，默认 null
    "small": { "provider": "glm", "model": "glm-4-flash" },
    "large": { "provider": "glm", "model": "glm-4.7" },
    "inputTokenThreshold": 24000
  }
}
```

token 估算：请求体字符数 / 4（中英混合近似），无需分词器。

**评价**

- ✅ 确定性：utility 识别与显式映射无启发式误判；虚拟名可跨供应商组合（flash 放便宜家）。
- ⚠️ autoRoute 仍属启发式，偶尔把复杂短任务误判到 flash（长输出、深推理但历史短），故默认关闭。

### 建议路径

1. **v0.2.0：确定性路由**——utility 识别 + 虚拟模型表 + `cops model use` + 托盘/管理页切换入口 + 未知 id 兜底。✅ **已交付**
2. **v0.2.1：autoRoute 作为可选叠加**（默认关闭）——仅在请求未命中前两级时启用启发式；
   上下文窗口元数据（`modelContext` / `contextWindow`）已就绪，可作为启发式的容量依据。
3. 共存优先级：**utility 模式 > 显式映射 > autoRoute 启发式 > 默认兜底**。

#### v0.2.0 交付勾稽（与原稿的差异）

- 四级实际为 **utility > virtual > pinned > default/passthrough**；autoRoute 完全未实现，
  连配置字段也未加（Go json 忽略未知字段，v0.2.1 加字段对旧配置无影响）；
- 第 3 级 `pinned`：`model == 钉住名（virtualModel）` 且 `defaultVirtual` 在表内 → 表目标；
  钉住名同时是表键时第 2 级优先（doctor 会给出提示）；
- `stripSampling` 落地为 **Provider 级开关**（默认关），命中路由 1-3 时取目标供应商的开关；
- `reqlog.Entry` 新增 `virtualModel` / `routeRule`（取值 utility/virtual/pinned/default/passthrough）；
  stats 聚合维度保持真实模型，`stats.json` 向后兼容；
- `/v1/models` 合成列表 = 钉住名（标注默认目标）+ 虚拟名（`cops_target` 标注）+ 激活供应商清单；
- 删除供应商时自动清理路由表中的悬空引用（`ScrubProviderReferences`）；
- `cops model` 写路径与 `switch` 一致：daemon 在线走管理 API（内存为准），离线本地 Save。

---

### 实施要点（v0.2.0）

| 模块 | 改动 |
|---|---|
| `internal/config` | 新增 `Routing{VirtualModels, DefaultVirtual, Utility, UtilityPatterns, AutoRoute}`；`Validate` 校验引用供应商存在、真实模型非空、`defaultVirtual` 在表内；旧配置无此字段时行为不变（全走默认兜底，等价 v0.1） |
| `internal/proxy` | `resolveTarget(reqModel)` 四级解析；`rewriteBody` / `handleModels` / `finish` 统一走该解析；未知 id 一律兜底；可选 `stripSampling` 剥离 `temperature:0` 等强制采样参数 |
| `/v1/models` | 合成列表 = 虚拟名（标注真实目标）+ 激活供应商真实模型清单；注明 CLI 本身不消费 |
| `internal/stats` / `reqlog` | `Entry` 增加 `virtualModel`、`routeRule`（utility/map/auto/default）；聚合维度保持真实模型，可按虚拟名/规则透视 |
| `internal/admin` + Web | providers API 扩展 routing 读写；管理页新增"路由"编辑区（虚拟模型三列表 + defaultVirtual 下拉 + utility 目标与模式） |
| `internal/cli` + 托盘 | `cops model use / add / list / remove`；托盘菜单在供应商之上增加虚拟模型快捷切换；`doctor` 校验路由表引用完整性 |
| 测试与文档 | httptest 覆盖：utility 命中/未命中/跨供应商/未知 id 兜底/autoRoute 阈值/stats 双记录；README 增补章节 |

### 风险与决策点

- **内部 id 随 CLI 版本漂移**（`gpt-5.4-nano` 只是当前观察值）：glob 模式 + 未知 id 全兜底策略，识别失效仅退化为"辅助调用走 pro"，不会 404；
- **plan 模式无法区分**：`/model plan` 在 BYOK 下同样不可用，计划请求与普通请求在代理侧无差别（除非 autoRoute 覆盖）；
- `COPILOT_MODEL`（钉住名）固定一个值，`cops model use` 改映射而非改环境变量——切换无需重开终端即生效；
- 强制采样参数（`temperature:0`）问题：`stripSampling` 默认关，README 说明何时开启（思考模型退化场景）；
- `stats.json` 聚合维度变化需向后兼容（旧文件可读）。
