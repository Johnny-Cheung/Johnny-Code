# Johnny-Code

使用 Go 逐步搭建的 Coding Agent CLI。当前 v0.1 实现单轮文本请求与流式输出，使用 OpenAI 兼容的 Chat Completions 协议连接模型服务。

## 运行环境

- Go 1.26 或更新版本。
- 支持 OpenAI Chat Completions 流式调用的模型服务，以及对应的 Key、API 根地址和模型名称。

## 配置

在项目根目录准备 `.env` 文件。若文件尚不存在，可复制模板：

```sh
cp .env.example .env
```

填写三个值：

```dotenv
JOHNNY_API_KEY=你的中转站Key
JOHNNY_BASE_URL=https://你的中转站地址/v1
JOHNNY_MODEL=中转站提供的模型名称
```

- `JOHNNY_API_KEY`：中转站提供的 Key。
- `JOHNNY_BASE_URL`：服务要求的 API 根地址。例如实际接口为 `https://relay.example/v1/chat/completions`，这里填写 `https://relay.example/v1`；如果实际地址使用其他路径前缀，则保留该前缀。程序会追加 `chat/completions`。
- `JOHNNY_MODEL`：中转站提供的模型标识，按其说明填写。

程序从当前目录读取 `.env`，终端中已设置的同名环境变量优先。`.env` 已被 Git 忽略，仓库只包含模板。

## 运行

在项目根目录执行：

```sh
go run ./cmd/johnny "你好，请用中文介绍一下你自己"
```

收到模型文本片段后立即输出；服务单独返回的推理字段不会作为正文显示。`Ctrl+C` 可以取消请求，整个请求最多等待 5 分钟。

模型正文写入标准输出，等待提示和错误写入标准错误。失败以非零状态退出，取消时退出码为 130；使用 `go run` 时退出状态由 Go 工具转述。

若网络需要代理，可在启动前设置 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY`，使用你本机实际代理地址。若 Go 默认依赖源不可访问，可以在此次命令前指定可用镜像，例如：

```sh
GOPROXY=https://goproxy.cn go run ./cmd/johnny "你好"
```

## 本地验证

```sh
go test ./...
go vet ./...
go build ./...
```

测试使用本地模拟服务验证配置加载、流式输出、取消和错误场景，不需要真实 Key。真实中转站是否兼容，需要填写配置后运行验收。

## 项目规划

- [总体路线图](docs/johnny-code-roadmap.md)
- [v0.1：模型调用](docs/versions/v0.1.md)
- [v0.2：全局 CLI 入口](docs/versions/v0.2.md)
