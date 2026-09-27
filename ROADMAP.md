# cops 迭代计划（ROADMAP）

> 已交付：**v0.1.0** 单激活供应商代理、一键热切换、install/doctor、Web 管理页、用量统计与请求日志；
> **v0.3 托盘常驻**（`cops tray`：启动自动注入环境变量、退出自动恢复并停止代理、托盘菜单快捷切换、附着/嵌入双模式、inject/restore 兜底命令）。

## 迭代 v0.2 —— 多模型路由（pro / flash 分工）

### 背景与动机

v0.1 的路由粒度是**供应商**：所有请求统一改写到当前激活供应商的单一 `model`。
但 Copilot CLI 本身支持分场景模型（主模型 / `/model plan` 计划模型 / `/model` 手选），
典型诉求是"重活走 pro、快任务走 flash"，一次配置后按请求自动分发，而不是每次手动 `cops switch`。

---

### 方案 A：多虚拟模型路由（按请求中的 model 名，确定性路由）

**思路**

- 配置一组**虚拟模型名 → (供应商, 真实模型)** 映射：

```jsonc
"virtualModels": {
  "cops-pro":   { "provider": "glm",      "model": "glm-4.7" },
  "cops-flash": { "provider": "glm",      "model": "glm-4-flash" },
  "cops-local": { "provider": "ollama",   "model": "qwen3:32b" }
}
```

- CLI 侧：主模型设 `cops-pro`，`/model plan cops-flash`；也可 `/model cops-flash` 临时手选。
- cops 按请求体中的 `model` 名路由到不同真实模型（**可跨供应商**）。
- `/v1/models` 合成列表也会列出这些虚拟名供选择。

**评价**

- ✅ 确定性、用户完全可控、无启发式误判；虚拟名可跨供应商组合（flash 放便宜家）。
- ⚠️ 需要用户在 Copilot CLI 侧做一次性 `/model` 配置。

---

### 方案 B：按请求规模自动路由（全自动，启发式）

**思路**

- cops 能看到完整请求体，可以按输入 token 估算路由：
  小上下文/短消息 → flash，大上下文/长历史 → pro。完全无感。
- 配置示例：

```jsonc
"autoRoute": {
  "enabled": true,
  "small":   { "provider": "glm", "model": "glm-4-flash" },
  "large":   { "provider": "glm", "model": "glm-4.7" },
  "inputTokenThreshold": 24000
}
```

- token 估算：请求体字符数 / 4（中英混合近似），无需引入分词器。

**评价**

- ✅ 零配置、完全无感。
- ⚠️ 属于启发式判断，偶尔会把复杂短任务误判到 flash（长输出、深推理但历史短的场景）；阈值难调。

---

### 建议路径（待确认）

1. **v0.2.0：方案 A**（确定性优先）——虚拟模型路由表 + 按请求 model 分发。
2. **v0.2.1：方案 B 作为可选叠加**（默认关闭）——仅在请求未命中虚拟表时启用启发式。
3. 共存优先级：**显式虚拟名 > autoRoute 启发式 > 激活供应商默认**。

---

### 实施要点（v0.2.0 · 方案 A）

| 模块 | 改动 |
|---|---|
| `internal/config` | 新增 `VirtualModels map[string]VirtualTarget{Provider, Model}`；`Validate` 校验引用的供应商存在、真实模型非空；旧配置无此字段时行为不变 |
| `internal/proxy` | 新增 `resolveTarget(reqModel)` → (provider, realModel, matched)；`rewriteBody` / `handleModels` / `finish` 统一走该解析；命中虚拟表时仍改写 model（`passthroughModel` 仅对未命中的兜底路径生效，文档说明） |
| `/v1/models` | 合成列表 = 虚拟名（标注真实目标）+ 激活供应商真实模型清单 |
| `internal/stats` / `reqlog` | `Entry` 增加 `virtualModel` 字段；聚合维度保持真实模型，同时可按虚拟名透视 |
| `internal/admin` + Web | providers API 扩展 virtualModels 读写；管理页增加"虚拟模型"编辑区（虚拟名/供应商/真实模型三列） |
| `internal/cli` | `cops model add <虚拟名> --provider --model` / `model list` / `model remove`；`doctor` 校验虚拟表引用完整性；`install` 输出提示 `/model plan cops-flash` 用法示例 |
| 测试与文档 | httptest 覆盖：命中/未命中/跨供应商/改写/stats 双记录；端到端冒烟更新；README 增补章节 |

### 风险与决策点

- `COPILOT_MODEL` 只能写一个默认虚拟名（建议 `cops-pro`）；plan 模型需用户在 CLI 内 `/model plan` 设置一次。
- Copilot CLI 对虚拟名的能力探测（上下文窗口等）展示的是虚拟名信息，不影响实际路由。
- `stats.json` 聚合维度变化需向后兼容（旧文件可读）。
- 方案 B 的阈值与"复杂短任务误判"问题：若误判率不可接受，仅保留方案 A。
