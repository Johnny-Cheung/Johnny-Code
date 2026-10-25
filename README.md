# Johnny-Code

使用 Go 逐步搭建的 Coding Agent CLI。v0.8 在 v0.7 权限体系上增加本地 Skills：从项目级和用户级目录发现 `SKILL.md`，系统提示只注入名称和描述，模型按需通过 `skill` 工具加载正文，也可以用 `/skill` 或独立 `/skill-name` 显式调用；Skill 只提供任务指导，不授予权限。v0.7 提供默认、只读、放行三种权限模式：默认模式自动执行路径白名单内的文件操作和固定查询命令，其余操作通过 TUI 逐次审批；支持项目命令前缀白名单、项目路径白名单和固定危险后缀黑名单。CLI 只支持在终端运行 `johnny` 进入 TUI，参数任务、管道和重定向会被拒绝。支持 Anthropic Messages、OpenAI Responses 和 OpenAI Chat Completions，可在任意目录启动并切换模型。v0.4 与 v0.5 已通过用户联合验收；v0.6 已通过用户验收；v0.8 已于 2026-10-25 确认冻结；v0.7 仍等待后续统一复核。

## 安装和更新

需要 Go 1.26 或更新版本。在源码目录执行：

```sh
./scripts/install.sh
```

安装脚本使用 `GOBIN`，未设置时使用 `GOPATH/bin`；通常安装为 `~/go/bin/johnny`。首次安装会补齐当前 shell 的 PATH，新开终端后即可使用。也可以按脚本提示，在当前终端加载所修改的 shell 配置文件。

脚本支持 zsh 和 Bash，尊重 `ZDOTDIR` 与已有 Bash 登录配置，并避免重复添加路径。目标位置存在其他程序时会停止，不覆盖该文件。

更新源码后，重新运行同一个脚本即可更新程序。构建失败时保留已安装的程序，现有 `.env` 始终保持原位和原内容。

## 使用

在任意目录的终端直接运行：

```sh
johnny
```

Johnny-Code 只支持以这种方式进入 TUI 交互模式。参数任务、管道和重定向会在读取配置前被拒绝，不会请求模型，也不会打开 `/dev/tty`。

- 每轮结束后继续等待输入，同一进程内保留用户消息、模型回复和工具结果。
- 输入完整的 `/exit` 或在空输入处按 `Ctrl+D`（EOF）正常退出；空行不发送请求。

TUI 操作：

| 操作 | 行为 |
| --- | --- |
| `Enter` | 发送当前任务 |
| `Alt+Enter` 或 `Ctrl+J` | 输入换行；Alt 组合键需要终端发送相应按键序列 |
| 粘贴多行文本 | 保留换行，按 Enter 后才提交；使用终端自带粘贴快捷键 |
| 左右方向键、Home/End、删除键 | 编辑输入 |
| 上下方向键 | 在多行内移动；位于首行/末行时切换本进程输入历史；斜杠菜单打开时改为选择菜单项 |
| 输入 `/` | 在输入框上方打开 Skill 和内置命令菜单；继续输入自动过滤 |
| 菜单中 `Enter` 或 `Tab` | 补全选中的命令；完整命令直接按 `Enter` 执行 |
| 菜单中 `Esc` | 关闭菜单；清空输入后重新输入 `/` 可再次打开 |
| 中文等输入法 | 组合文本和候选窗口跟随输入框中的真实光标位置 |
| 任务执行中 | 输入框上方实时逐条显示工具名、路径或命令和执行状态 |
| 本轮结束按 `Ctrl+O` | 展开最近一次工具过程；默认折叠为一行摘要 |
| 任务中 `Ctrl+C` | 取消本轮，等待当前操作停止后继续输入；上下文和已完成修改保留 |
| 审批等待中 `↑`/`↓`、`k`/`j`、`Enter` | 选择“允许本次 / 拒绝本次 / 批准并记录”；默认焦点在拒绝 |
| 审批等待中 `Ctrl+C` | 取消本轮，不执行待审批操作 |
| `/permissions` | 查看固定白名单、危险后缀黑名单和项目规则；支持 `remove-command`、`remove-path`、`clear` |
| `/skills` | 查看已加载、被项目级覆盖、未加载和未注入提示的 Skill |
| `/skills reload` | 重新扫描项目级和用户级 Skill 目录，并更新后续轮次的系统提示 |
| `/skill <name> [参数]` | 显式使用某个 Skill；正文直接进入本轮任务，终端只显示原始命令 |
| 空闲时 `Ctrl+C` | 清空输入，保留会话 |
| `/exit`、空输入 `Ctrl+D` | 正常退出 |

权限模式：

```sh
johnny                                      # 默认模式
johnny --permission-mode read-only
johnny --permission-mode allow
```

- `default`：不传 `--permission-mode` 时使用。启动工作目录和项目路径白名单内 `read`、`write`、`edit` 自动执行；`echo`、`pwd`、`ls`、`find`、`grep`、`cat`、`head`、`tail`、`wc`、`stat`、`du`、`diff` 的受限形式自动执行；其余文件访问、Bash 命令通过审批。
- 自动执行必须同时满足：未命中危险后缀黑名单；每个命令段命中用户级查询白名单或已记录的项目前缀；可识别的路径参数都在项目路径白名单内。
- 默认模式支持受控的 `cd <目录> && <简单命令>` 链，以及简单的 `||`、`;` 控制运算符，最多 8 段；每段独立检查，`cd` 只允许出现在第一段。满足条件时整条链按直接 argv 调用执行，不经过 shell；`&&`、`;` 可记录，含 `||` 的链只能本次允许。默认模式的管道仍进入一次性审批，不提供记录；重定向、后台执行、命令替换、循环和脚本解释器同样需要审批。
- 项目命令前缀只接受固定工具族的安全子命令，例如 `git status`、`git log`、`go test`、`go build`、`cargo test`、`npm run build`、`make test`。首次审批选择“批准并记录”后写入 `.johnny/permissions.local.json`，后续同前缀自动执行；解释器、shell、危险后缀不提供记录。
- 项目前缀命令会检查形似路径的参数和常见选项值；脚本、测试和构建目标内部访问仍无法静态限制，这是应用层策略的边界。
- 审批只有“允许本次”“拒绝本次”“批准并记录”三项；默认焦点在拒绝。等待审批期间 `Ctrl+C` 取消本轮。参数任务、管道和重定向在入口处直接拒绝，不会进入 TUI 或打开 `/dev/tty`。
- 项目规则文件加载前会展示内容并请求确认，确认后的内容哈希保存在 `~/.johnny/trusted_permissions.json`；文件变化后重新确认。
- `read-only`：拒绝 `write`、`edit`；`read` 只能读取启动工作目录内的文件（允许读取 `.env` 等敏感文件）；`bash` 只允许上述 12 个查询命令的受限形式，不经过 shell，并额外允许两段固定查询管道，例如 `find . -name '*.go' | head -n 20`、`ls | wc -l`；管道消费者不能带路径，仍拒绝重定向、`&&`、`||`、`;` 和其他复合命令。不做 shell 通配符展开，按名称查找文件请用 `find -name`。
- `allow`：直接执行工具（包括 `skill`），不进行逐次确认，必须显式选择。
- 三种模式都是应用层策略，不是操作系统沙箱。默认模式的 Bash 仍以当前用户权限运行，脚本内部行为不受路径检查限制；只读模式允许读取范围内的 `.env`、私钥、`.git` 等文件但拒绝写入；放行模式不应用路径规则。默认模式路径白名单内的 `.env`、私钥、`.git` 和 `.johnny` 权限文件可以直接读写，这是明确取舍。默认模式的子进程环境会过滤 Johnny 和模型服务凭据变量，但不能阻止命令读取磁盘上的配置。
- 默认模式不是“所有操作都自动”，也不是“所有操作都弹窗”：低风险、可解释的操作保持流畅，写入、任意命令和范围外访问由用户决定。

## Skills

Skill 是任务指导包，不是新的执行引擎。它告诉模型某类任务应遵循的步骤和项目约定；Skill 不能授予工具权限，真正的文件读写和命令执行仍受当前权限模式管理。

发现位置：

```text
<启动工作目录>/.johnny/skills/<name>/SKILL.md     # 项目级，优先
~/.johnny/skills/<name>/SKILL.md                  # 用户级
```

`SKILL.md` 使用最小 frontmatter：

```markdown
---
name: go-test
description: 当需要运行 Go 测试、定位失败用例或补充回归测试时使用。
---

# Go 测试流程

1. 先运行 `go test ./...`
2. 失败时按包定位
```

- 启动时只解析名称、描述和正文；系统提示只注入名称、描述和来源，正文不进入上下文。
- 输入 `/` 会在输入框上方打开斜杠菜单，列出全部 Skill 和内置命令；继续输入按名称过滤，`↑`/`↓` 选择，`Enter` 或 `Tab` 补全，`Esc` 关闭。选中后会补全为 `/skill-name ` 并留一个空格，便于继续输入参数。
- 每个 Skill 同时是独立的 `/skill-name` 命令，例如 `/hello-file`、`/report-maker`；原来的 `/skill <name> [参数]` 继续保留。输入完整命令后直接 `Enter` 执行；只输入前缀时第一次 `Enter` 补全，第二次执行。
- 内置命令优先于同名 Skill；例如 Skill 名为 `exit` 时，`/exit` 仍然是退出，使用该 Skill 需要输入 `/skill exit`。
- 模型判断任务匹配后调用 `skill` 工具加载完整正文；`/skill <name> [参数]` 可以直接显式加载，正文进入本轮任务但不写入终端滚动历史。
- 项目级与用户级同名时项目级覆盖用户级；项目级 Skill 自动加载，不额外请求信任确认；无效项不会阻断启动，`/skills` 显示原因。
- `/skills reload` 重新扫描并更新后续轮次的系统提示；任一目录无法读取时保留原集合。
- 最多 64 个 Skill；名称和描述等元数据最多 8 KiB；单个正文 128 KiB；描述 1024 字节。名称只能使用小写字母、数字和连字符，并与目录名一致。
- `allowed-tools` 在 v0.8 静默忽略，不改变工具集合。
- 用户级 Skill 的引用文件位于工作目录外，只读模式下不可读；需要引用文件时建议放在项目级目录。
- `.johnny/*` 默认不进入仓库，但 `.johnny/skills/` 保留，项目级 Skill 可以随项目提交。

任务执行期间不排队提交新任务。界面显示模型、工作目录、等待/接收/执行/取消状态。执行期间，工具调用会在输入框上方按顺序实时显示，使用终端可用高度；超过一屏时保留最新记录并在顶部提示更早条数。本轮结束时清除实时进展，在最终回答前保留一行折叠摘要（已执行次数、工具名，按 `Ctrl+O` 展开），随后显示完整的最终回答。完整工具调用与结果保留在会话 JSONL 中；需要回看时用 `Ctrl+O` 展开最近一次过程。模型回复按 Markdown 渲染标题、列表、引用、表格和代码块；明确语言的代码块包含语法高亮，未标注或纯文本代码块按无高亮等宽原文显示，长行按词换行。目录树有单独的排版规则：无围栏也能识别，不进行语法高亮；宽度足够时保持原始列，窄终端压缩数字列并把说明换行到说明列或条目下方，树形分支不会被拆断。调用链、关系图等自由排版内容保留原缩进换行，长说明不会被省略号截断。当前使用深色终端样式，暂不提供主题切换。生成期间状态区显示已接收字数，不把回答原文写入动态区域；响应结束后先提交折叠摘要，再提交排版后的正文，避免重复显示。单次输入最多 10000 行，超限粘贴会整体拒绝并提示，不静默截断。

例如，第一轮输入“创建一个输出 hello 的 Go 程序”，下一轮可以继续输入“把刚才的输出改成 world 并运行检查”。后一次请求会携带当前会话历史。

每轮内部都可以多次请求模型和执行工具。交互模式中，模型请求失败、截断或达到次数上限会说明原因，然后继续等待输入。TUI 中的按键 `Ctrl+C` 只取消当前轮；外部 SIGINT、SIGTERM 或终端挂断信号会取消任务并退出。新进程从新会话开始，不加载旧记录。

临时切换模型或协议，不修改 `.env`：

```sh
johnny --model "服务提供的另一个模型名"
johnny --api openai-responses --model "服务提供的GPT模型名"
johnny --api openai-chat-completions --model "服务提供的模型名"
```

启动后直接在 TUI 中输入问题。选项放在启动命令上；包含空格的模型名用引号包围。`--model` 只替换模型名，`--api` 只替换协议，服务地址和 Key 继续使用当前配置。跨服务切换时，用 `JOHNNY_BASE_URL`、`JOHNNY_API_KEY` 环境变量提供相应配置。每次运行仍只访问一个服务，不自动探测或切换协议。

本地查询：

```sh
johnny --help
johnny --version
```

帮助显示实际配置位置；版本包含版本号与构建时的 Git 提交号，构建包含未提交改动时附加 `dirty` 标识。帮助和版本查询不读取模型配置、不访问模型服务。

程序保持启动时的工作目录。日常运行直接启动已构建的二进制，不经过 `go run`；网络和模型首段响应的等待时间独立于本地启动时间。

## 复用已有配置

已配置好的源码项目根目录 `.env` 继续使用，无需搬动、复制或重新填写。安装脚本只将该文件的位置写入构建信息，不读取或编译其中的凭据。修改配置内容后立即对下一次问答生效；源码目录位置变化后，重新运行安装脚本以更新定位。

第一次克隆且尚无配置时，才需要参考 `.env.example` 创建 `.env`，填写：

```dotenv
JOHNNY_API_KEY=你的中转站Token
JOHNNY_BASE_URL=https://你的中转站地址
JOHNNY_MODEL=中转站提供的模型名称
```

- `JOHNNY_API_KEY`：通过 `Authorization: Bearer` 发送。Anthropic 模式对应 `ANTHROPIC_AUTH_TOKEN` 认证方式。
- `JOHNNY_BASE_URL`：服务根地址或以 `/v1` 结尾的 API 根地址，程序按协议追加接口路径，避免重复 `/v1`。不支持填入完整接口地址。
- `JOHNNY_MODEL`：服务提供的模型标识。
- `JOHNNY_API`：可选协议字段，未配置时为 `anthropic-messages`。

| JOHNNY_API | 调用接口 |
| --- | --- |
| `anthropic-messages` | `/v1/messages` |
| `openai-responses` | `/v1/responses`，使用 `store: false` |
| `openai-chat-completions` | `/v1/chat/completions` |

命令行 `--api`、`--model` 优先于相应环境变量；`JOHNNY_*` 环境变量按字段优先于文件值。显式空值也会覆盖配置并报错；可选协议字段只有在完全未提供时才使用默认值。其他应用的 Anthropic/OpenAI 凭据不会覆盖本项目认证。`.env` 被 Git 忽略，仓库只包含模板。

每次模型请求最多等待 5 分钟、最多输出 4096 个 token；每轮用户输入默认最多请求模型 50 次，可用 `--max-turns 正整数` 调整（选项放在启动命令上），下一轮重新计算预算。任务不设固定总超时。最后一次允许的响应仍请求工具时，不再执行这批工具，明确提示达到上限；截断、拒绝或请求失败也会清楚停止本轮，不自动重试。Bash 默认超时 2 分钟，工具参数可指定 1～600 秒。

模型正文在 TUI 中流式接收并按 Markdown 渲染；思考字段不作为正文显示。工具名称、路径或命令、成功或失败状态，以及有限结果预览在执行期间显示在输入框上方，本轮结束后折叠为一行摘要，可用 `Ctrl+O` 展开；日志路径和警告也经界面显示。成功退出码为 0，失败为 1，进程取消为 130；TUI 内取消单轮后仍保留进程，之后正常退出为 0。

会话调试记录保存到 `~/.johnny/sessions/<会话ID>.jsonl`，第一轮开始执行时显示路径；同一交互会话共用一个文件，记录完整消息、工具调用及结果、请求用量和停止原因。元数据包含启动工作目录、协议、模型和 `permission_mode`；不写入 API Key 或环境变量配置。审批决策额外记录为 `permission` 事件，包含工具调用 ID、目标、允许/拒绝/记录、来源、原因以及实际记录的命令前缀数组或路径。`user_turn` 表示第几次用户输入，`turn` 表示该轮内第几次模型请求。记录目录权限为 0700、文件为 0600；记录失败会警告并降级继续任务。记录只用于排查，不自动加载旧会话，不做记忆处理或会话恢复。

例如，在用于验证的空目录中运行 `johnny`，输入：

```text
用 write 创建输出 hello 的 main.go，用 read 读取，用 edit 将 hello 改成 world，再用 bash 执行 go run main.go 并检查输出。
```

默认模式下 `go run` 会请求审批；也可以启动 `johnny --permission-mode allow` 直接执行。所有入口都必须是终端 TUI，参数任务和管道会被拒绝。

若网络需要代理，启动前设置本机适用的 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`。Go 默认依赖源不可访问时，可以临时为安装命令指定可用镜像：

```sh
GOPROXY=https://goproxy.cn ./scripts/install.sh
```

## 开发与本地验证

开发阶段仍可在源码目录运行，此时默认读取当前目录 `.env`：

```sh
go run ./cmd/johnny
```

验证命令：

```sh
go test -race ./...
go vet ./...
go build ./...
bash -n scripts/install.sh
```

测试使用临时配置、临时 HOME 和本地模拟服务，不读取真实 Key。覆盖三种协议、模型切换、流式事件与完整消息、工具消息转换、结束原因、用量、错误和取消；工具注册、文件读写、编辑、Bash 取消超时、后台进程清理与输出限制；Agent 多工具串行执行、错误回传、请求预算和每次请求超时；默认模式的 12 个只读查询、`&&`/`||`/`;` 控制运算符、只读查询管道、路径和危险规则、项目规则信任哈希、TUI 三选项审批与入口拒绝；Skill 目录发现、frontmatter 校验、项目覆盖用户、数量与元数据预算、`skill` 工具权限放行、`/skills` 重载、`/skill` 本地展开、斜杠菜单过滤/选择/补全/关闭、独立 `/skill-name` 命令、PTY 自动与显式调用；调试记录、批量审批审计与写入失败降级；以及跨目录配置复用、安装、更新、PATH 幂等与碰撞保护。三种协议的循环集成测试使用真实文件工具，并核对 Replay 和工具结果 ID。

统一模型层位于 `internal/model`，独立工具层位于 `internal/tool`。`internal/agent` 负责本轮消息历史、模型与工具调度及结束判断；`internal/session` 负责只写不读的 JSONL 调试记录；`internal/skill` 负责本地 Skill 发现、frontmatter 校验、项目覆盖用户、元数据预算、系统提示区块和重载。`internal/tui` 使用 Bubble Tea v2、Bubbles textarea 和 Lip Gloss v2 管理终端事件、输入、状态、审批面板与滚动输出，斜杠菜单在 `internal/tui/slash_menu.go`，Markdown 由 glamour v2 渲染，目录树由 `internal/tui/tree.go` 按列识别和排版；输入框使用真实终端光标，中文输入法组合文本会出现在输入位置。CLI 负责将有界显示数据与 Agent 事件衔接。工具开始状态更新后等待一个渲染周期并检查终端输出错误，再继续执行；界面输出失败会取消任务。系统提示要求目录树等对齐文本使用 text 围栏代码块，渲染层同时兼容无围栏或语言标注不合适的目录树。只读模式由 `internal/tool/readonly.go` 包装工具，工作目录边界在 `internal/tool/scope.go`，受限查询执行器在 `internal/tool/restricted_bash.go`；默认模式在 `internal/tool/default.go` 和 `internal/tool/bash_chain.go`，审批接口在 `approval.go`，项目规则在 `project_rules.go`，固定白名单和危险后缀分别在 `permission_whitelist.go`、`permission_blacklist.go`；`skill` 工具在 `internal/tool/skill.go`。它仍是工具策略，不是系统沙箱。

手动验证工具：

```sh
go run ./cmd/tool-demo
go run ./cmd/tool-demo bash '{"command":"pwd"}'
go run ./cmd/tool-demo write '{"path":"demo.txt","content":"hello"}'
go run ./cmd/tool-demo read '{"path":"demo.txt"}'
```

## 项目规划

- [总体路线图](docs/johnny-code-roadmap.md)
- [v0.1：模型调用](docs/versions/v0.1.md)
- [v0.2：全局 CLI 入口](docs/versions/v0.2.md)
- [v0.3：统一模型接口](docs/versions/v0.3.md)
- [v0.4：工具系统](docs/versions/v0.4.md)
- [v0.5：Agent Loop](docs/versions/v0.5.md)
- [v0.6：TUI](docs/versions/v0.6.md)
- [v0.7：安全与权限（已实现，等待验收）](docs/versions/v0.7.md)
- [v0.8：Skills（已确认冻结）](docs/versions/v0.8.md)
- [v0.9：MCP（讨论中）](docs/versions/v0.9.md)
