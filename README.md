# Johnny-Code

**在终端里读代码、改文件、跑命令的 Coding Agent，用 Go 写成。**

![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)
![Version](https://img.shields.io/badge/version-v0.13.0-blue)

在任意项目目录运行 `johnny`，用自然语言描述任务，它会反复调用文件与命令工具——读实现、改代码、跑测试、查日志——直到给出结论。支持 Anthropic Messages、OpenAI Responses 和 OpenAI Chat Completions 三种协议，可以接自建中转站。

```text
$ johnny
> 给 calc 加一个 pow 命令，补测试并运行

  正在执行  bash   go test ./...
  正在编辑  calc.go

✓ 本轮完成 · 9 次工具调用 · bash, read, edit        Ctrl+O 展开

已完成：pow 命令实现在 calc.go，3 个测试全部通过。
────────────────────────────────────────
> █
────────────────────────────────────────
空闲 · Enter 发送 · Alt+Enter 换行 · /exit 退出
```

> 界面示意；实际布局会随终端宽度、运行状态和任务区内容变化。

## 特性

- **终端原生 TUI**：流式回复、Markdown 与语法高亮、真实终端光标、中文输入法、工具过程折叠与回看。
- **三种权限模式**：默认逐次审批、只读、放行；审批可只允许本次、本会话内允许或写入项目规则。
- **受控命令执行**：21 个固定查询命令、项目命令前缀、最多 16 段的管道与 `&&`/`||`/`;` 分段器，不经过 shell。
- **后台子 Agent**：`fresh` 或 `fork` 起点，最多 5 个并行；结果自动注入下一轮或唤醒空闲的主 Agent。
- **Workflow**：主 Agent 先声明分步大纲，再逐步并行执行，整步报告合并送回，适合前后依赖的任务。
- **上下文治理**：32 KiB 工具预览、大输出存档、接近窗口时自动压缩，`/context` 与 `/compact` 可查可控。
- **Skills 与长期记忆**：项目/用户级 `SKILL.md`、AGENTS 规则、跨会话 memory，按需加载而非全量注入。
- **可排查**：每轮对话和工具调用写入本地 JSONL，审批原因同样留痕；记录只用于排查，不参与恢复。

## 快速开始

需要 **Go 1.26 或更新版本**，macOS 或 Linux。Windows 需要手动构建，见[开发](docs/development.md)。

```sh
git clone git@github.com:Johnny-Cheung/Johnny-Code.git
cd Johnny-Code
cp .env.example .env      # 填入服务地址、Key 和模型名
./scripts/install.sh
```

安装脚本把程序装到 `GOBIN`（未设置时是 `~/go/bin/johnny`），首次安装会把该目录加入 zsh/Bash 的 PATH。新开终端后：

```sh
cd /path/to/your/project
johnny
```

更新代码后重新执行 `./scripts/install.sh` 即可，`.env` 原地保留、内容不动。

### 配置

```dotenv
JOHNNY_API_KEY=你的 Token
JOHNNY_BASE_URL=https://你的中转站地址
JOHNNY_MODEL=服务提供的模型名
# JOHNNY_API=anthropic-messages   # 可选
```

| 变量 | 说明 |
| --- | --- |
| `JOHNNY_API_KEY` | 以 `Authorization: Bearer` 发送 |
| `JOHNNY_BASE_URL` | 服务根地址或以 `/v1` 结尾的地址；不要填 `/v1/messages` 这类完整接口路径 |
| `JOHNNY_MODEL` | 服务实际提供的模型标识 |
| `JOHNNY_API` | `anthropic-messages`（默认）、`openai-responses`、`openai-chat-completions` |

`.env` 被 Git 忽略，仓库只带 `.env.example`。环境变量按字段优先于文件，`--api` / `--model` 优先于两者。安装脚本把 **clone 目录下 `.env` 的路径**写进二进制，所以 clone 目录不要移动或删除；移动后重新安装即可，也可以完全改用环境变量。

更多配置项、目录和日志说明见[配置](docs/configuration.md)。

## 使用

```sh
johnny           # 默认模式：逐次审批
johnny --read    # 只读模式
johnny --skip    # 放行模式：跳过逐次审批
```

`--permission-mode default|read-only|allow` 是等价的长写法，和 `--read`/`--skip` 同时使用时必须一致。

| 模式 | 行为 |
| --- | --- |
| `default` | 工作目录内的文件读写、固定查询命令自动执行；其余操作逐次审批 |
| `read-only` | 拒绝写入；`bash` 只允许固定查询；不弹审批，直接拒绝 |
| `allow` | 工具直接执行，不逐次确认 |

常用操作：

| 操作 | 行为 |
| --- | --- |
| `Enter` / `Alt+Enter` | 发送任务 / 输入换行 |
| `↑` `↓` | 浏览本次输入历史；斜杠菜单打开时改为选择 |
| `/` | 打开 Skill 与内置命令菜单 |
| `Ctrl+O` | 展开或折叠最近一次工具过程 |
| `Ctrl+C` | 取消本轮和全部子任务；空闲时清空输入 |
| `/exit`、空输入 `Ctrl+D` | 退出 |

内置命令：`/permissions`、`/context`、`/compact`、`/memory`、`/remember`、`/skills`、`/skills reload`、`/skill <name>`。每个 Skill 同时是独立的 `/skill-name` 命令。

完整按键、审批面板和任务区说明见[使用手册](docs/usage.md)。

### 示例

```text
用 read 看一下 internal/tool 的目录结构，找出 bash 工具的实现入口
```

```text
给 calc 增加 pow 命令，补一个表驱动测试，然后运行 go test ./...
```

```text
只读检查这个大仓库的风险点：并发、错误处理和配置读取，输出一份报告
```

默认模式下写文件和任意命令会请求审批。选择「批准并记录」后，同一项目前缀（如 `go test`）写入 `.johnny/permissions.local.json`，后续自动执行。

## 权限模型

三种模式都是**应用层策略，不是操作系统沙箱**。

- 默认模式自动执行需要同时满足：路径在工作目录白名单内、未命中危险后缀、每个命令段命中固定查询表 / 项目前缀 / 本会话授权。
- 固定查询 21 个：纯读 14 个（`echo`、`basename`、`dirname`、`pwd`、`realpath`、`ls`、`grep`、`cat`、`head`、`tail`、`wc`、`stat`、`du`、`diff`），逃逸口 7 个（`find`、`rg`、`sed`、`sort`、`uniq`、`tree`、`file`），各自使用封闭参数表。
- 默认与只读模式共用分段器：支持 `|`、`&&`、`||`、`;`、虚拟 `cd` 和三种尾部安全重定向，最多 16 段，按 argv 直接执行。
- 未引用的通配符直接拒绝，查找文件请用 `find -name`；超过 16 段也直接拒绝。
- 审批提供允许本次、拒绝本次、本会话内允许、批准并记录；默认焦点在拒绝。

默认模式下常见自动执行：

```sh
rg -n "TODO" . | head -20
find . -name '*.go' | head
git status 2>/dev/null        # 已记录 git status 前缀后
go test ./... | tail -20      # 已记录 go test 前缀后
```

路径规则、危险后缀、项目规则信任、只读边界等完整细节见[权限模型](docs/permissions.md)和 [v0.7 设计文档](docs/versions/v0.7.md)。

## 子 Agent 与 Workflow

**SubAgent。** 主 Agent 通过 `task` 把边界清晰的任务交给后台子 Agent：`fresh` 从干净上下文开始，`fork` 带上主对话快照。子任务运行期间主 Agent 可以继续工作或结束本轮；报告在下一次请求前注入，主 Agent 空闲时自动唤醒汇总。写入类子任务必须声明文件范围，`write`/`edit` 不能越界。

**Workflow。** 较大的任务由主 Agent 用 `workflow` 拆成有顺序的几步：先 `start` 声明大纲，轮到某一步时再 `run` 启动这一步的多个并行任务，整步结束后所有报告合并成一条送回，最后一步完成即收尾。步骤必须按顺序执行，大纲不能中途修改。`end` 可提前结束。

```text
workflow 重构日志模块  2/4
✓ 1 调研现状
◆ 2 并行改造各子模块    3/5 个任务完成
    ◆ 改造 parser · 1m05s · 运行中
    ◆ 改造 writer · 1m05s · 等待审批
○ 3 集成测试
○ 4 汇总
```

详见 [v0.12 · SubAgent](docs/versions/v0.12.md) 和 [v0.13 · Workflow](docs/versions/v0.13.md)。

## 上下文、记忆与 Skills

- **上下文治理**：完整历史、请求视图和终端展示分离；单次工具结果模型可见上限 32 KiB，超出部分存档并可续读；接近窗口上限时保留最近 5 个已完成轮次并生成摘要。`/context` 查看用量，`/compact` 手动压缩。
- **长期记忆**：`~/.johnny/AGENTS.md` 与项目根 `AGENTS.md` 固定注入；记忆一条一个 Markdown 文件，`MEMORY.md` 索引常驻、正文按需读取。模型通过只读 `memory` 检索、通过 `remember` 写入。
- **Skills**：从 `<项目>/.johnny/skills/` 和 `~/.johnny/skills/` 发现 `SKILL.md`，系统提示只注入名称和描述，正文由 `skill` 工具按需加载。Skill 只提供任务指导，不授予权限。

## 文档

| 文档 | 内容 |
| --- | --- |
| [使用手册](docs/usage.md) | 按键、内置命令、审批、任务区、输出与退出 |
| [配置](docs/configuration.md) | `.env`、协议、环境变量、会话日志与本地目录 |
| [权限模型](docs/permissions.md) | 三种模式、固定查询、分段器、审批与边界 |
| [开发](docs/development.md) | 本地运行、测试、代码结构和手动验证工具 |
| [路线图](docs/johnny-code-roadmap.md) | 版本顺序、范围与各版验收状态 |
| [版本文档](docs/versions/) | v0.1 – v0.13 的设计、取舍和验收记录 |

## 开发

```sh
go run ./cmd/johnny                 # 源码目录直接运行，读取当前目录 .env
go test -race ./...                 # 全量测试
go vet ./... && go build ./...
bash -n scripts/install.sh
```

测试使用临时配置、临时 HOME 和本地模拟服务，不读取真实凭据。代码分层、测试覆盖和手动验证工具见[开发](docs/development.md)。

Windows 未做验证；安装脚本只支持 zsh/Bash，可尝试手动构建：

```powershell
go build -o johnny.exe ./cmd/johnny
```

## 状态

当前版本 **v0.13.0**。除暂缓的 MCP（v0.9）外，v0.1 – v0.13 均已实现并完成验收；MCP 的设计讨论保留在版本文档中。`johnny` 默认进入逐次审批，`--read` 选择只读，`--skip` 选择放行。

Johnny-Code 是一个逐步迭代的个人项目：每个版本先讨论、再实现、再一起验收，设计取舍和限制都记录在[版本文档](docs/versions/)里。
