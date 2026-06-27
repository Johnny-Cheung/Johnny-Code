# Johnny Code

一个运行在终端中的 AI 编程助手，基于 Go + Bubble Tea 构建。支持多模型、子代理、技能系统、团队协作、MCP、记忆管理等能力。

## 快速开始

### 环境要求

- Go 1.25+
- Git

### 安装

```bash
# 1. 克隆项目
git clone <repo-url>
cd "Johnny Code"

# 2. 配置 API
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入你的 API 信息

# 3. 启动
go run ./cmd/johnnycode/
```

首次启动时，程序会自动将项目根目录的 `config.yaml` 复制到 `~/.johnnycode/config.yaml`，之后在任意目录下输入 `johnnycode` 即可启动。

### 全局安装（可选）

```bash
go install ./cmd/johnnycode/
johnnycode   # 任意目录直接启动
```

## 配置

项目根目录的 `config.yaml` 示例：

```yaml
providers:
  - name: deepseek               # 任意名称
    protocol: anthropic          # anthropic | openai | openai-compat
    base_url: https://api.anthropic.com
    model: claude-sonnet-4-6
    api_key: sk-xxx              # 你的 API Key

permission_mode: default         # default | accept-all | plan
```

配置加载优先级（后者覆盖前者）：

1. `~/.johnnycode/config.yaml` — 用户全局
2. `./config.yaml` — 项目根目录
3. `.johnnycode/config.yaml` — 项目隐藏目录
4. `.johnnycode/config.local.yaml` — 本地覆盖（不提交 git）

## 功能

### 核心工具
文件读写、精确编辑、Shell 命令执行、文件搜索 (Glob)、内容搜索 (Grep)，带权限控制和路径沙箱。

### 多模型支持
支持 Anthropic、OpenAI 及 OpenAI 兼容接口。可配置多个 provider，启动时切换。

### 权限模式
`Shift+Tab` 循环切换：
- **default** — 写文件和命令需确认
- **acceptEdits** — 文件编辑自动通过，命令需确认
- **plan** — 只读模式，不允许写操作
- **YOLO** — 全部自动通过

### 子代理系统
三种内置代理：
- `general-purpose` — 全功能，200 轮限制
- `plan` — 只读，架构设计专用
- `explore` — 只读，快速代码搜索

支持在 `.johnnycode/agents/` 中定义自定义代理。

### 技能系统
Markdown 格式的 SOP，支持内联和 fork 两种执行模式。通过 `/技能名` 调用。

技能目录结构 `~/.johnnycode/skills/<name>/SKILL.md`，可安装、可自定义工具。

### 团队协作
支持多代理团队：Leader 负责协调，队友 (teammates) 并行执行任务。支持 in-process、tmux、iTerm2 三种运行模式，团队成员间可互相通信。

### MCP 协议
完整支持 Model Context Protocol，可对接 stdio 或 HTTP 类型的 MCP Server，为代理扩展外部工具能力。

### 记忆系统
自动提取和保存用户偏好、反馈、项目上下文。支持双路径：用户级（`~/.johnnycode/memory/`）和项目级（`.johnnycode/memory/`）。

### 工作区隔离
子代理可在独立 git worktree 中运行，避免并发编辑冲突。自动清理无变化的 worktree。

### 上下文压缩
自动检测 token 用量，接近窗口上限时压缩历史消息——保留最近对话原文，总结早期内容。

### 会话管理
支持会话恢复、上下文回退 (rewind) 到任意检查点。

## 自定义指令

可以在以下位置创建指令文件，代理会自动加载：

- `~/.johnnycode/JOHNNYCODE.md`
- `~/.johnnycode/AGENTS.md`
- 项目级 `JOHNNYCODE.md` / `AGENTS.md`
- `.johnnycode/INSTRUCTIONS.md`

支持 `@include` 指令引用其他文件。

## 系统快捷键

| 快捷键 | 功能 |
|--------|------|
| `Shift+Tab` | 切换权限模式 |
| `Ctrl+O` | 展开/折叠工具输出 |
| `↑` / `↓` | 浏览历史命令 |

## 项目结构

```
cmd/johnnycode/      入口
internal/
  tui/               Bubble Tea 终端界面
  agent/             代理核心循环
  agents/            子代理定义与调度
  skills/            技能系统
  teams/             团队协作
  tools/             工具注册与实现
  mcp/               MCP 协议客户端
  memory/            记忆系统
  worktree/          Git worktree 管理
  compact/           上下文压缩
  llm/               LLM 客户端（Anthropic/OpenAI/兼容）
  config/            配置加载
  permissions/       权限系统
  hooks/             生命周期钩子
  commands/          斜杠命令
  session/           会话持久化
```

## License

MIT
