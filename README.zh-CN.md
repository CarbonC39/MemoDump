# MemoDump

<p align="center">
  <a href="README.md">English</a> | 简体中文
</p>

<p align="center">
  <img src="frontend/public/memodump.svg" alt="MemoDump logo" width=150/>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" />
  <img alt="License" src="https://img.shields.io/badge/License-MIT-green" />
  <img alt="PWA" src="https://img.shields.io/badge/PWA-ready-5A0FC8?logo=pwa&logoColor=white" />
  <img alt="Wails" src="https://img.shields.io/badge/Desktop-Wails-red?logo=go" />
  <img alt="Docker" src="https://img.shields.io/badge/Docker-ghcr.io-2496ED?logo=docker&logoColor=white" />
</p>

<p align="center">
  <a href="https://memodump.carbonc.cc/">官网</a> ·
  <a href="https://memodump.vercel.app/">在线演示</a>
</p>

一个轻量的单文件 Markdown 笔记应用。可以作为自托管的 Web 服务器运行，也可以作为原生桌面应用（基于 [Wails](https://wails.io/)）运行，或者直接用 Docker 容器部署。

> [在线演示](https://memodump.vercel.app/) 运行在无鉴权模式下，数据存储是临时的，仅用于体验编辑器——请不要在上面存放重要内容。

## 特性

- **单文件二进制** —— Go 后端内嵌 Vue 3 前端，放到任意位置即可运行。
- **Markdown 编辑器** —— 基于 [Milkdown](https://milkdown.dev/) 的所见即所得编辑器，支持完整 Markdown 语法。
- **文件夹组织** —— 支持拖拽的层级文件夹结构，以及 `.md` 文件导入。
- **全文搜索** —— 基于内存的快速全文搜索，支持同时对正文和标签进行 AND 查询。
- **瀑布流卡片视图** —— 可视化瀑布流式笔记浏览，与文件夹树并列展示。
- **自动保存与离线队列** —— 静默自动保存，底层使用 IndexedDB 存储；离线时产生的编辑会在网络恢复后自动回放。
- **字体预设与排版控制** —— 内置 system、serif、sans 三种字体族预设，支持自定义 CSS 栈，可独立调整应用界面、WYSIWYG 编辑器和纯文本编辑器的字号。
- **设置面板** —— 全页设置视图，带实时预览卡片、数值输入框和排版控制。
- **自定义 CSS** —— 可通过 `--css` 命令行参数注入样式表，也可在设置面板中直接在线编辑自定义 CSS。
- **灵活的鉴权方式** —— 支持用户名/密码会话鉴权，也可在个人/可信网络环境下使用无鉴权模式。
- **分层配置** —— 命令行参数 → 环境变量 → `.env` 文件（可任意组合）。
- **桌面应用** —— 通过 Wails 提供原生窗口，复用同一套代码，无需浏览器。
- **移动端/PWA 友好** —— 响应式设计，可作为 PWA 安装，支持返回导航。

<p align="center">
  <img src="images/md-editor.avif" alt="Markdown 编辑器视图"/>
  <img src="images/waterfall-view.avif" alt="瀑布流笔记视图"/>
</p>

---

## CLI 服务器

### 快速开始

```sh
# 无鉴权（个人/可信网络环境使用）
memodump --data ./notes

# 带账号密码
memodump --data ./notes --user alice --pass secret

# 自定义端口
memodump --data ./notes --user alice --pass secret --port 9090
```

在浏览器中打开 `http://localhost:8080`（或自定义端口）。

### 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--data` | 笔记数据目录路径（必填） | — |
| `--user` | 登录用户名 | — |
| `--pass` | 登录密码 | — |
| `--port` | HTTP 端口 | `8080` |
| `--css` | 注入到界面中的自定义 CSS 文件 | — |

### 配置来源

配置可以通过三种方式提供，按优先级从高到低：

| 优先级 | 来源 | 说明 |
|--------|------|------|
| 1（最高） | 命令行参数 | `--user alice --pass secret` |
| 2 | 环境变量 | `MEMODUMP_DATA`、`MEMODUMP_USER`、`MEMODUMP_PASS`、`MEMODUMP_PORT`、`MEMODUMP_CSS` |
| 3（最低） | 工作目录下的 `.env` 文件 | `DATA=`、`USER=`、`PASS=`、`PORT=`、`CSS=` |

**.env 文件示例**

```env
DATA=./notes
USER=alice
PASS=secret
PORT=9090
CSS=./custom.css
```

以 `#` 开头的行和空行会被忽略。值不会做引号去除处理。

### 无鉴权模式

如果所有来源都没有配置账号密码，服务器会以**无鉴权模式**启动——所有 API 接口无需会话 Cookie 即可访问。

```sh
memodump --data ./notes
# WARNING: No credentials configured — running in no-auth mode (all requests allowed)
```

---

## 桌面应用（Wails）

Wails 构建版本将同一套后端包装进原生窗口——无需浏览器，也不会监听端口。

- 首次启动时会自动解析数据目录（不会弹窗询问）：
  1. 二进制文件同目录或工作目录下 `.env` 文件中的 `DATA=` 键
  2. 工作目录下的 `./data` 子目录（不存在则自动创建）
- 选定的路径会保存到操作系统的用户配置目录，后续启动时复用。
- 可通过侧边栏的**数据文件夹**按钮选择其他目录（需要重启生效）。
- 始终运行在无鉴权模式下（账号密码在此模式下不适用）。

**配置文件位置**

| 操作系统 | 路径 |
|----------|------|
| Windows | `%APPDATA%\memodump\config.json` |
| macOS | `~/Library/Application Support/memodump/config.json` |
| Linux | `~/.config/memodump/config.json` |

---

## Docker

每次打 tag 发布时，预构建镜像都会发布到 GitHub Container Registry：`ghcr.io/carbonc39/memodump`。该镜像只包含无界面的 CLI 服务器（Wails 桌面版不适用于容器场景）。

```sh
# 无鉴权
docker run -d -p 8080:8080 -v ./notes:/data ghcr.io/carbonc39/memodump:latest

# 带账号密码和自定义端口
docker run -d -p 9090:9090 -v ./notes:/data \
  -e MEMODUMP_USER=alice -e MEMODUMP_PASS=secret -e MEMODUMP_PORT=9090 \
  ghcr.io/carbonc39/memodump:latest
```

数据卷挂载到镜像内的 `/data`（镜像内已设置 `MEMODUMP_DATA=/data`）。可用标签：`latest`、`vX.Y.Z`、`vX.Y`。所有 [CLI 环境变量](#配置来源) 在容器内同样适用。

本地构建镜像：`docker build -t memodump .`（参见 `Dockerfile`）。

---

## 构建

### 前置依赖

- Go 1.25+
- Node 20+（含 npm）

### CLI 服务器

```sh
# 构建前端
cd frontend && npm install && npm run build && cd ..

# 构建当前平台版本
go build -o memodump .

# 交叉编译示例（Linux arm64）
GOOS=linux GOARCH=arm64 go build -o memodump-linux-arm64 .
```

### 桌面应用（Wails）

```sh
# 安装 Wails CLI（仅需一次）
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 生产构建
wails build

# 开发模式（热重载）
wails dev
```

输出位于 `build/bin/`。

> **注意：** `wails dev` 会打开一个用于热重载代理的终端窗口，这是正常现象。生产构建（`wails build`）在 Windows 上使用 `-H windowsgui`，生成无控制台窗口的纯 GUI 二进制文件。

### 构建标签参考

| 标签 | 使用场景 | 入口文件 |
|------|----------|----------|
| *(无)* | `go build .` | `main_cli.go` |
| `production` | `wails build` | `main_wails.go` |
| `dev` | `wails dev` | `main_wails.go` |
| `bindings` | Wails JS 绑定生成（内部使用） | `main_wails.go` |

---

## 项目结构

```
memodump/
├── main_cli.go       # CLI 入口（构建标签：!production && !dev && !bindings）
├── main_wails.go     # Wails 入口（构建标签：production || dev || bindings）
├── app_wails.go      # Wails App 结构体——启动、数据目录配置、切换文件夹对话框
├── server.go         # 共享：包级变量 + buildAPIMux()
├── api.go            # 笔记与文件夹 API 处理器
├── auth.go           # 会话鉴权中间件与登录/登出处理器
├── wails.json         # Wails 项目配置
├── frontend/         # Vue 3 + Milkdown 前端（通过 embed 内嵌进二进制文件）
│   └── src/
│       ├── views/MainView.vue
│       └── style.css
├── Dockerfile        # 无界面 CLI 服务器镜像（多阶段构建：前端 → go build → distroless）
└── .github/workflows/build.yml   # CI/CD（主要流程）——见下文
```

---

## CI / CD

CI 运行在 **GitHub Actions**（`.github/workflows/build.yml`），在每次推送/PR 到 `public` 分支以及打 `v*` tag 时触发。

### `build-cli` —— CLI 交叉编译

每次推送/PR 都会运行（成本低，基于 Linux 的交叉编译）：

| 目标平台 | 输出文件 |
|----------|----------|
| Linux amd64 | `memodump-server-linux-amd64` |
| Linux arm64 | `memodump-server-linux-arm64` |
| Linux arm | `memodump-server-linux-arm` |
| Windows amd64 | `memodump-server-windows-amd64.exe` |
| Windows 386 | `memodump-server-windows-386.exe` |
| macOS amd64 | `memodump-server-darwin-amd64` |
| macOS arm64 | `memodump-server-darwin-arm64` |

### `build-desktop` —— Wails 桌面构建

仅在打 `v*` tag 或手动触发时运行（原生 macOS/Windows/Linux runner 计费更高，因此不会在每次推送时运行）：Windows amd64、macOS universal、Linux amd64。

### `docker` —— Docker 镜像

同样仅在打 `v*` tag 或手动触发时运行。构建 `linux/amd64` + `linux/arm64`，推送到 `ghcr.io/carbonc39/memodump`，标签为 `latest`、`vX.Y.Z` 和 `vX.Y`。

### `release` —— GitHub Release

仅在打 `v*` tag 时触发。将所有构建产物收集到一个 GitHub Release 中。

---

<p align="center">
  如果 MemoDump 对你有所帮助，不妨<a href="https://ko-fi.com/carbonc">请我喝杯热巧 ☕</a>
</p>
