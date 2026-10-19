# Johnny-Code

使用 Go 逐步搭建的 Coding Agent CLI。v0.5 接通统一模型接口与 `bash`、`read`、`edit`、`write` 四个工具：输入一个任务后，程序执行模型请求的工具、回传结果并继续请求模型，直到模型正常结束。支持 Anthropic Messages、OpenAI Responses 和 OpenAI Chat Completions，可在任意目录启动并切换模型。v0.4 与 v0.5 已实现，待用户联合验收。

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

也可以先启动，再输入一个问题：

```sh
johnny
```

两种方式均处理一个用户任务；任务内部可以多次请求模型、执行工具并回传结果，最终回答后退出。无参数模式输入空行会提示错误；没有输入时按 `Ctrl+D` 退出，按 `Ctrl+C` 取消输入、模型请求或当前工具，停止后续调度。

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

每次模型请求最多等待 5 分钟、最多输出 4096 个 token；一次任务默认最多请求模型 50 次，可用 `--max-turns 正整数` 调整（选项放在任务之前）。任务不设固定总超时。最后一次允许的响应仍请求工具时，不再执行这批工具，明确提示达到上限；截断、拒绝或请求失败也会清楚停止，不自动重试。Bash 默认超时 2 分钟，工具参数可指定 1～600 秒。

模型正文流式写入标准输出，思考字段不作为正文显示。工具名称、路径或命令、成功或失败状态，以及有限结果预览写入标准错误；预览最多 20 行、4096 字节，截断只影响展示，不改变回传模型的结果。成功退出码为 0，失败为 1，取消为 130。

每次任务的调试记录保存到 `~/.johnny/sessions/<会话ID>.jsonl`，启动时显示实际路径，记录完整消息、工具调用及结果、请求用量和停止原因。记录目录权限为 0700、文件为 0600；元数据不写入 API Key 或环境变量配置。记录失败会警告并降级继续任务。记录只用于排查，不自动加载旧任务，不做记忆处理或会话恢复。

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

统一模型层位于 `internal/model`，独立工具层位于 `internal/tool`。`internal/agent` 负责本轮消息历史、模型与工具调度及结束判断；`internal/session` 负责只写不读的 JSONL 调试记录。CLI 将模型事件用于正文显示，将工具事件用于状态显示和记录。四个工具沿用当前直接执行方式，权限确认与隔离留到 v0.7。

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
