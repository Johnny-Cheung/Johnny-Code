# Johnny-Code

使用 Go 逐步搭建的 Coding Agent CLI。v0.2 支持本地安装、任意目录启动，以及单轮文本问答和流式输出；模型服务使用 Anthropic 原生 Messages 协议。

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

两种方式均只处理一个问题，流式输出后退出。无参数模式输入空行会提示错误；没有输入时按 `Ctrl+D` 退出，按 `Ctrl+C` 取消输入或模型请求。

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

- `JOHNNY_API_KEY`：通过 `Authorization: Bearer` 发送，对应 `ANTHROPIC_AUTH_TOKEN` 认证方式。
- `JOHNNY_BASE_URL`：服务根地址，SDK 追加 `v1/messages`。例如实际接口为 `https://relay.example/v1/messages`，这里填写 `https://relay.example`。
- `JOHNNY_MODEL`：中转站提供的模型标识。

`JOHNNY_*` 环境变量按字段优先于文件值；显式设置为空也会覆盖文件并被报告为缺失。其他应用的 Anthropic 凭据不会覆盖本项目认证。`.env` 被 Git 忽略，仓库只包含模板。

请求最多等待 5 分钟，回复上限为 4096 个输出 token，思考字段不作为正文显示。正文写入标准输出，输入提示、等待状态和错误写入标准错误。成功退出码为 0，失败为 1，取消为 130。

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

测试使用临时配置和本地模拟服务，不读取真实 Key。覆盖原生协议、流式输出、取消、单轮输入、跨目录配置复用，以及隔离目录中的安装、更新、PATH 幂等与碰撞保护。

## 项目规划

- [总体路线图](docs/johnny-code-roadmap.md)
- [v0.1：模型调用](docs/versions/v0.1.md)
- [v0.2：全局 CLI 入口](docs/versions/v0.2.md)
