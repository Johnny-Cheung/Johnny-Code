# Johnny-Code

使用 Go 逐步搭建的 Coding Agent CLI。v0.6 在统一模型接口、四个基础工具和同进程多轮对话之上，增加随终端滚动的 TUI：流式查看回复和工具进展，编辑或粘贴多行任务，取消当前轮后继续对话。支持 Anthropic Messages、OpenAI Responses 和 OpenAI Chat Completions，可在任意目录启动并切换模型。v0.4 与 v0.5 已通过用户联合验收；v0.6 已实现，待用户验收。

## 安装和更新

需要 Go 1.26 或更新版本。在源码目录执行：

```sh
./scripts/install.sh
```

安装脚本使用 `GOBIN`，未设置时使用 `GOPATH/bin`；通常安装为 `~/go/bin/johnny`。首次安装会补齐当前 shell 的 PATH，新开终端后即可使用。也可以按脚本提示，在当前终端加载所修改的 shell 配置文件。

脚本支持 zsh 和 Bash，尊重 `ZDOTDIR` 与已有 Bash 登录配置，并避免重复添加路径。目标位置存在其他程序时会停止，不覆盖该文件。

更新源码后，重新运行同一个脚本即可更新程序。构建失败时保留已安装的程序，现有 `.env` 始终保持原位和原内容。

## 使用

在任意目录直接提问：

```sh
johnny "你好，请用中文介绍一下 Go 语言"
```

也可以进入多轮交互：

```sh
johnny
```

- `johnny "任务"`：只执行这一个任务，完成或失败后退出，不读取后续标准输入。
- `johnny`：每轮结束后继续等待输入，同一进程内保留用户消息、模型回复和工具结果。标准输入、输出都是终端时启用 TUI；管道或重定向继续使用普通行模式。输入完整的 `/exit` 或在空输入处按 `Ctrl+D`（EOF）正常退出；空行不发送请求。

TUI 操作：

| 操作 | 行为 |
| --- | --- |
| `Enter` | 发送当前任务 |
| `Alt+Enter` 或 `Ctrl+J` | 输入换行；Alt 组合键需要终端发送相应按键序列 |
| 粘贴多行文本 | 保留换行，按 Enter 后才提交；使用终端自带粘贴快捷键 |
| 左右方向键、Home/End、删除键 | 编辑输入 |
| 上下方向键 | 在多行内移动；位于首行/末行时切换本进程输入历史，向下返回未发送草稿 |
| 任务中 `Ctrl+C` | 取消本轮，等待当前操作停止后继续输入；上下文和已完成修改保留 |
| 空闲时 `Ctrl+C` | 清空输入，保留会话 |
| `/exit`、空输入 `Ctrl+D` | 正常退出 |

任务执行期间不排队提交新任务。界面显示模型、工作目录、等待/接收/执行/取消状态；回复和工具摘要进入终端滚动历史。Markdown 与代码块保留可读原文，本版没有代码高亮或工具折叠面板。单次输入最多 10000 行，超限粘贴会整体拒绝并提示，不静默截断。

例如，第一轮输入“创建一个输出 hello 的 Go 程序”，下一轮可以继续输入“把刚才的输出改成 world 并运行检查”。后一次请求会携带当前会话历史。

每轮内部都可以多次请求模型和执行工具。交互模式中，模型请求失败、截断或达到次数上限会说明原因，然后继续等待输入。TUI 中的按键 `Ctrl+C` 只取消当前轮；单任务和普通行模式中的 `Ctrl+C` 信号仍取消并退出整个进程。外部 SIGINT、SIGTERM 或终端挂断信号会取消任务并退出。新进程从新会话开始，不加载旧记录。

临时切换模型或协议，不修改 `.env`：

```sh
johnny --model "服务提供的另一个模型名" "请用一句话介绍自己"
johnny --api openai-responses --model "服务提供的GPT模型名" "请回复：调用成功"
johnny --api openai-chat-completions --model "服务提供的模型名" "你好"
```

选项放在问题之前；包含空格的模型名用引号包围。`--model` 只替换模型名，`--api` 只替换协议，服务地址和 Key 继续使用当前配置。跨服务切换时，用 `JOHNNY_BASE_URL`、`JOHNNY_API_KEY` 环境变量提供相应配置。每次运行仍只访问一个服务，不自动探测或切换协议。

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

每次模型请求最多等待 5 分钟、最多输出 4096 个 token；每轮用户输入默认最多请求模型 50 次，可用 `--max-turns 正整数` 调整（选项放在任务之前），下一轮重新计算预算。任务不设固定总超时。最后一次允许的响应仍请求工具时，不再执行这批工具，明确提示达到上限；截断、拒绝或请求失败也会清楚停止本轮，不自动重试。Bash 默认超时 2 分钟，工具参数可指定 1～600 秒。

普通文本模式中，模型正文流式写入标准输出，思考字段不作为正文显示。工具名称、路径或命令、成功或失败状态，以及有限结果预览写入标准错误；预览最多 20 行、4096 字节，截断只影响展示，不改变回传模型的结果。TUI 将这些内容统一交给界面显示，文件工具成功时给出简短摘要，命令结果或失败信息提供有限预览；日志路径和警告也经界面显示。成功退出码为 0，失败为 1，进程取消为 130；TUI 内取消单轮后仍保留进程，之后正常退出为 0。

会话调试记录保存到 `~/.johnny/sessions/<会话ID>.jsonl`，第一轮开始执行时显示路径；同一交互会话共用一个文件，记录完整消息、工具调用及结果、请求用量和停止原因。`user_turn` 表示第几次用户输入，`turn` 表示该轮内第几次模型请求。记录目录权限为 0700、文件为 0600；元数据不写入 API Key 或环境变量配置。记录失败会警告并降级继续任务。记录只用于排查，不自动加载旧会话，不做记忆处理或会话恢复。

例如，在用于验证的空目录中运行：

```sh
johnny --max-turns 10 "用 write 创建输出 hello 的 main.go，用 read 读取，用 edit 将 hello 改成 world，再用 bash 执行 go run main.go 并检查输出。"
```

若网络需要代理，启动前设置本机适用的 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`。Go 默认依赖源不可访问时，可以临时为安装命令指定可用镜像：

```sh
GOPROXY=https://goproxy.cn ./scripts/install.sh
```

## 开发与本地验证

开发阶段仍可在源码目录运行，此时默认读取当前目录 `.env`：

```sh
go run ./cmd/johnny "你好"
```

验证命令：

```sh
go test -race ./...
go vet ./...
go build ./...
bash -n scripts/install.sh
```

测试使用临时配置、临时 HOME 和本地模拟服务，不读取真实 Key。覆盖三种协议、模型切换、流式事件与完整消息、工具消息转换、结束原因、用量、错误和取消；工具注册、文件读写、编辑、Bash 取消超时、后台进程清理与输出限制；Agent 多工具串行执行、错误回传、请求预算和每次请求超时；调试记录与写入失败降级；以及跨目录配置复用、安装、更新、PATH 幂等与碰撞保护。三种协议的循环集成测试使用真实文件工具，并核对 Replay 和工具结果 ID。

统一模型层位于 `internal/model`，独立工具层位于 `internal/tool`。`internal/agent` 负责本轮消息历史、模型与工具调度及结束判断；`internal/session` 负责只写不读的 JSONL 调试记录。`internal/tui` 使用 Bubble Tea、Bubbles textarea 和 Lip Gloss 管理终端事件、输入、状态与滚动输出，CLI 负责将有界显示数据与 Agent 事件衔接。工具开始行实际写入终端成功后才继续执行；界面输出失败会取消任务。四个工具沿用当前直接执行方式，权限确认与隔离留到 v0.7。

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
