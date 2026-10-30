# 开发

## 本地运行

在源码目录运行，默认读取当前目录的 `.env`：

```sh
go run ./cmd/johnny
```

日常使用建议安装成二进制，不经过 `go run`：

```sh
./scripts/install.sh
```

## 验证

```sh
go test -race ./...
go vet ./...
go build ./...
bash -n scripts/install.sh
```

测试使用临时配置、临时 HOME 和本地模拟服务，不读取真实 Key，也不访问真实模型服务。三种协议的循环集成测试使用真实文件工具，并核对 Replay 和工具结果 ID。

覆盖范围：

- **模型与协议**：三种协议、模型切换、流式事件与完整消息、工具消息转换、结束原因、用量、错误和取消。
- **工具层**：注册、文件读写、编辑、Bash 取消与超时、后台进程清理、输出限制；`task` 参数、写入文件范围登记与双向拦截。
- **Agent Loop**：多工具串行执行、错误回传、请求预算、每次请求超时、收件箱注入与合并上限、空闲唤醒入口、fork 起点截断与拒绝过长上下文。
- **上下文治理**：估算、32 KiB 预览、轮内预览预算、bash 存档与清理、轮级压缩、摘要前缀替换、90%/95% 触发、摘要失败降级、上下文超限和 thrashing 熔断；`/context`、`/compact` 和摘要事件日志。
- **权限**：21 个固定查询（纯读 14、逃逸口 7）、声明式参数表、平台相关的 `stat`/`file` 选项、16 段分段器、管道与 `&&`/`||`/`;` 的原始退出码短路、`cd` 约束、三种尾部重定向、未识别 shell 语法的一次性审批、通配符与超长命令拒绝、只读复合查询、路径和危险规则、项目规则信任哈希、审批面板四选项、会话授权与队列重检、`/permissions`、入口拒绝。
- **Skill**：目录发现、frontmatter 校验、项目覆盖用户、数量与元数据预算、`skill` 工具权限放行、`/skills` 重载、`/skill` 本地展开、斜杠菜单过滤/选择/补全/关闭、独立 `/skill-name` 命令、PTY 自动与显式调用。
- **长期记忆**：AGENTS 加载、条目格式、project-id、索引预算、用户/项目存储、`remember` 工具、用户规则、轮末审查、会话事件、`/memory`、`/remember` 和 PTY 记忆流程。
- **SubAgent**：并发上限、时长和请求次数限额、报告截断与数据声明、取消与 bash 进程清理、审批来源与等待状态、审批先进先出队列、唤醒保险阀、`Ctrl+C` 与退出语义；PTY 中的后台运行、自动唤醒、输入时不抢占、默认模式审批和退出取消。
- **Workflow**：大纲登记与顺序约束、整批校验与回滚、步骤屏障与整步合并报告、排队补位与取消丢弃、报告总量预算、只读与默认模式、任务区布局与折叠、步骤唤醒保险阀；PTY 中的分步推进与 `Ctrl+C` 取消。
- **其他**：会话记录与批量审批审计（含请求原因）、写入失败降级、默认模式审批引导提示、`--read`/`--skip` 短选项与长写法冲突、跨目录配置复用、安装与更新、PATH 幂等与碰撞保护。

既有 PTY 计时用例偶发失败、单独重跑通过；这是重构前已存在的问题。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `internal/model` | 统一模型层与三种协议适配 |
| `internal/tool` | 工具定义、执行、权限、路径边界、受控命令、Skill 工具、`task` 与 `workflow` 工具 |
| `internal/agent` | Agent Loop、消息历史、工具调度、摘要请求、收件箱与结束判断 |
| `internal/contextview` | 预算与 token 估算、工具结果预览与存档、请求视图、轮级压缩、thrashing 保护 |
| `internal/memory` | AGENTS 加载、用户/项目记忆、索引、显式写入和旁路审查 |
| `internal/session` | 只写不读的 JSONL 调试记录 |
| `internal/skill` | Skill 发现、frontmatter 校验、项目覆盖用户、元数据预算、系统提示区块和重载 |
| `internal/subagent` | 子任务登记、并发与限额、fresh/fork 起点、报告格式化、workflow 进度账本与步骤屏障 |
| `internal/tui` | Bubble Tea v2 界面、输入、状态、审批面板、任务区、Markdown 与目录树渲染 |
| `cmd/johnny` | CLI 入口、参数、接线、系统提示、TUI 启动与日志 |
| `cmd/tool-demo` | 手动调用单个工具的小工具 |

关键文件：

- 受限查询：`internal/tool/restricted_bash.go`
- 默认模式判定：`internal/tool/default.go`、`bash_chain.go`
- 受控链执行：`internal/tool/controlled_exec.go`
- 审批与会话授权：`approval.go`、`session_grants.go`
- 项目规则与信任：`project_rules.go`
- 固定白名单与危险后缀：`permission_whitelist.go`、`permission_blacklist.go`
- 工作流：`internal/subagent/workflow.go`、`workflow_step.go`、`internal/tool/workflow.go`、`internal/tui/workflow.go`、`workflow_area.go`

## 手动验证工具

```sh
go run ./cmd/tool-demo
go run ./cmd/tool-demo bash '{"command":"pwd"}'
go run ./cmd/tool-demo write '{"path":"demo.txt","content":"hello"}'
go run ./cmd/tool-demo read '{"path":"demo.txt"}'
```

## 安装脚本

`./scripts/install.sh`：

- 使用 `GOBIN`，未设置时使用 `GOPATH/bin`；通常安装为 `~/go/bin/johnny`。
- 把 clone 目录 `.env` 的路径和 Git 提交号写进构建信息；不读取配置内容。
- 首次安装补齐当前 shell 的 PATH；支持 zsh 和 Bash，尊重 `ZDOTDIR` 与已有 Bash 登录配置，避免重复添加。
- 目标位置已有其他程序时停止，不覆盖。
- 构建失败时保留已安装的程序，`.env` 始终保持原位和原内容。
- 更新源码后重新运行同一个脚本即可。

其他 shell 会提示需要手动加入的目录。Windows 不支持该脚本，也尚未在 Windows 上验证；可以尝试手动构建：

```powershell
go build -o johnny.exe ./cmd/johnny
```

此时程序默认读取启动目录的 `.env`，不再使用安装脚本写入的固定路径。
