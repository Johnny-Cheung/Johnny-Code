# 配置

## 配置文件的位置

默认读取启动目录下的 `.env`。通过 `./scripts/install.sh` 安装时，脚本会把 **clone 目录下 `.env` 的绝对路径**写进二进制，因此安装后可以在任意项目目录运行 `johnny`，配置仍指向同一份文件。

- 修改 `.env` 内容后立即对下一次问答生效，不需要重新安装。
- clone 目录移动或删除后，配置路径失效；重新运行安装脚本即可更新，或改用环境变量。
- `johnny --help` 最后一行显示当前二进制实际使用的配置文件路径。
- `.env` 被 Git 忽略，仓库只包含 `.env.example`；安装脚本只写入路径，不读取或嵌入其中的凭据。

## 字段

```dotenv
JOHNNY_API_KEY=你的 Token
JOHNNY_BASE_URL=https://你的中转站地址
JOHNNY_MODEL=服务提供的模型名
JOHNNY_API=anthropic-messages
```

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `JOHNNY_API_KEY` | 是 | 通过 `Authorization: Bearer` 发送；Anthropic 模式对应 `ANTHROPIC_AUTH_TOKEN` 认证方式 |
| `JOHNNY_BASE_URL` | 是 | 服务根地址或以 `/v1` 结尾的 API 根地址；程序按协议追加接口路径，避免重复 `/v1`。不支持填写完整接口地址 |
| `JOHNNY_MODEL` | 是 | 服务实际提供的模型标识 |
| `JOHNNY_API` | 否 | 未配置时为 `anthropic-messages` |

协议与接口：

| `JOHNNY_API` | 调用接口 |
| --- | --- |
| `anthropic-messages` | `/v1/messages` |
| `openai-responses` | `/v1/responses`，使用 `store: false` |
| `openai-chat-completions` | `/v1/chat/completions` |

## 优先级

1. 命令行 `--api`、`--model`；
2. `JOHNNY_*` 环境变量（按字段）；
3. `.env` 文件值；
4. 可选协议字段的默认值。

显式空值也会覆盖配置并报错；可选协议字段只有在完全未提供时才使用默认值。其他应用的 Anthropic/OpenAI 凭据不会覆盖本项目认证。

完全不用 `.env` 也可以，直接导出环境变量：

```sh
export JOHNNY_API_KEY=...
export JOHNNY_BASE_URL=https://...
export JOHNNY_MODEL=...
johnny
```

## 临时切换模型或协议

不修改 `.env`：

```sh
johnny --model "服务提供的另一个模型名"
johnny --api openai-responses --model "服务提供的 GPT 模型名"
johnny --api openai-chat-completions --model "服务提供的模型名"
```

每次运行只访问一个服务，不自动探测或切换协议。跨服务切换时，用 `JOHNNY_BASE_URL`、`JOHNNY_API_KEY` 环境变量提供相应配置。

## 其他环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `JOHNNY_CONTEXT_WINDOW` | `1000000` | 模型上下文窗口 `W` |
| `JOHNNY_MAX_OUTPUT_TOKENS` | `8192` | 每次请求的输出预留 `O`；子 Agent 不低于 8192。窗口较小时未显式设置的值会按窗口八分之一降低 |
| `JOHNNY_REQUEST_TIMEOUT` | `10m` | 单次模型请求的总时限，使用 Go 时长格式，例如 `90s`、`20m` |
| `JOHNNY_BASH_TIMEOUT` | `10m` | `bash` 和固定查询的默认超时，最大 `1h`；工具参数 `timeout_seconds` 仍可覆盖单次调用 |
| `JOHNNY_MEMORY_AUTO=off` | 开 | 关闭轮末自动记忆审查；显式 `remember`、`/remember` 和 `/memory` 仍可用 |

预算口径：`S = max(16384, W × 5%)`，可用输入预算 `U = W - O - S`。默认输出预留为 8192；窗口较小时会按窗口自动降低默认输出预留，避免挤空输入。

## 超时、重试与次数

- 每次模型请求默认最多等待 10 分钟，可用 `JOHNNY_REQUEST_TIMEOUT` 调整。
- 模型返回 429、5xx 或网络错误时自动指数退避重试，默认 3 次；服务端提供 `Retry-After` 时优先遵守，最长等待 30 秒。已经向终端输出过内容的流不会重试，避免重复显示。
- 每轮用户输入默认最多请求模型 100 次，可用 `--max-turns` 调整；下一轮重新计算。
- 达到上限时，最后一次响应里请求的工具不再执行，并明确提示。
- Bash 默认超时 10 分钟，可用 `JOHNNY_BASH_TIMEOUT` 调整；工具参数 `timeout_seconds` 可指定 1～3600 秒。
- 任务不设固定总超时。主 Agent 每次请求默认输出预留 8192 token；子任务默认最多 100 次模型请求、30 分钟总时长，单次请求沿用主会话的请求超时；子任务输出上限不低于 8192 token。请求次数和时长是防止子任务挂死占住并发名额的保险丝，不是任务预算，撞上限后会标注部分结果并结束。
- 单份子任务报告正文最多 16 KiB；workflow 一步内所有任务报告合计最多 96 KiB，任务多时每份相应变短。写大文件或长报告时，主 Agent 会在一次 `write` 中控制约 8 KiB，再用 `append=true` 追加，避免工具参数撞上输出上限。
- `fork` 允许父请求视图最多占可用输入预算的 70%；超过时提示主 Agent 改用 `fresh`。

## 上下文与长期内容预算

- 单次工具结果模型可见预览最多 32 KiB；当前轮的累计预览预算从 128 KiB 起，随可用输入预算放大，用尽后后续结果保留 16 KiB 预览并给出存档或续读路径。
- 自动摘要保留最近 8 个已完成轮次；摘要输出上限 8192 token，截断或格式不完整时会改用精简指令重试一次。摘要失败时先保留最近轮次并压缩工具结果，仍放不下才退到确定性裁剪。
- 轮级压缩后的工具结果预览为 2 KiB；完整内容仍在 JSONL 和工具输出存档中。
- 用户级 `AGENTS.md` 最多 200 行 / 2000 token，项目级最多 500 行 / 4000 token；超限会截断并给出警告。
- 用户级记忆索引最多 2000 token，项目级最多 4000 token；每个作用域最多 256 条记忆。
- Skill 元数据最多注入 32 KiB 的描述；超出后只列出名称，模型仍可用 `skill` 工具按名称加载。

## 会话记录

调试记录保存到：

```text
~/.johnny/sessions/<会话ID>.jsonl
```

- 第一轮开始执行时在界面显示路径；同一交互会话共用一个文件。
- 记录完整消息、工具调用及结果、请求用量和停止原因。
- 元数据包含启动工作目录、协议、模型和 `permission_mode`；不写入 API Key 或环境变量配置。
- 审批决策记录为 `permission` 事件，包含工具调用 ID、目标、允许/拒绝/记录、来源、决策原因、请求原因（`request_reason`、`request_detail`）以及实际记录的命令前缀数组或路径。
- `user_turn` 表示第几次用户输入，`turn` 表示该轮内第几次模型请求。
- 目录权限 0700、文件 0600；写入失败会警告并降级继续任务。
- 记录可通过 `/resume` 或 `--resume <会话ID>` 恢复；恢复时只载入最近一次摘要和摘要之后的保留轮次，更早的细节仍留在文件里，供模型的 `history` 工具按需查询。
- 恢复会加排他文件锁，避免两个进程同时追加同一份记录。最后一条 `end` 之后的不完整内容会被截断丢弃。
- 记录不自动成为长期记忆；恢复不还原「本会话内允许」授权、子 Agent 或 Workflow。

子 Agent 和 workflow 任务各有独立 JSONL，文件名为 `<主会话ID>-sub-<子任务ID>.jsonl`，元数据包含父会话、子任务 ID、`kind`、`access`、`paths`、描述和所属步骤。主会话记录 `subagent_start`、`subagent_end`、`inject` 以及 `workflow_*` 事件。

## 本地目录

```text
~/.johnny/
├── AGENTS.md                    # 用户级规则
├── memory/                      # 用户级记忆
│   ├── MEMORY.md
│   └── <name>.md
├── projects/<project-id>/memory/  # 项目级记忆
├── sessions/                    # 会话 JSONL
├── skills/<name>/SKILL.md       # 用户级 Skill
└── trusted_permissions.json     # 项目规则信任哈希

<项目>/
├── AGENTS.md                    # 项目规则
└── .johnny/
    ├── permissions.local.json   # “批准并记录”写入的项目规则
    ├── artifacts/<会话ID>/      # bash 大输出存档
    └── skills/<name>/SKILL.md   # 项目级 Skill（可随仓库提交）
```

- `.johnny/*` 默认不进入仓库，但 `.johnny/skills/` 例外，项目级 Skill 可以提交。
- 工具输出存档目录权限 0700、文件 0600；正常退出时删除当前会话的存档。
- 工具结果存档不是长期记忆，也不用于会话恢复；会话恢复只读取 JSONL。

## 网络

需要代理时，启动前设置本机适用的 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`。

安装或更新时如果 Go 默认依赖源不可访问，可以临时指定镜像：

```sh
GOPROXY=https://goproxy.cn ./scripts/install.sh
```

## 本地查询

```sh
johnny --help
johnny --version
```

帮助显示实际配置位置；版本包含版本号与构建时的 Git 提交号，构建包含未提交改动时附加 `dirty` 标识。两者都不读取模型配置、不访问模型服务。
