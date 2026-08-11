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
- **图片粘贴与上传** —— 粘贴/拖拽图片即插入；默认存到本地图库，也可配置 S3 兼容图床。
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

## 图片支持

三种形态的图片存储方式：

| 形态 | 默认 | 可配置 |
|------|------|--------|
| Web 服务器 | 本地图库（`<dataDir>/.images/`） | S3 兼容（设置面板或环境变量） |
| Wails 桌面 | 本地图库（`<dataDir>/.images/`） | S3 兼容（设置面板） |
| 纯前端 / PWA | 关闭（仅图片链接） | S3 兼容（设置面板，浏览器直传） |

- 在编辑器中粘贴或拖拽图片文件即可插入。默认存到本地图库，markdown 里保存相对 URL（`/api/images/<key>`），只在应用自身源内可解析——这是自托管图片的可移植性取舍。
- **S3 模式要求 bucket 对公网可读**（否则图片会显示 403），纯前端直传还需配置 bucket 的 CORS（允许应用域名、`PUT/POST/GET/HEAD`、`Content-Type` 与 `x-amz-*` 请求头，并暴露 `ETag` 以支持分片上传）。
- **隐私提示**：S3 模式下图片可通过链接公开访问；内容哈希不是访问控制，相同文件会生成相同的链接。请勿上传需要私密保存的图片。
- 离线粘贴的图片会先保存在浏览器 IndexedDB 中，联网后自动上传；图片只有在上传且公开可读后才从队列移除（少量孤儿对象可能残留，属预期行为）。
- **可选的定期清理**（设置 → 图片）：开启后，服务器会定期删除未被任何笔记引用的图片（本地图库与 S3，带 7 天宽限期；S3 模式下删除为远程且不可恢复），Web/Wails 构建还会在 30 天后清除永久失败的上传记录。建议为 MemoDump 使用独立的 bucket/prefix，避免影响其他文件。默认关闭。
- 安全：图片 key 为 `sha256(内容) + 规范扩展名`（JPEG 统一 `.jpg`），服务端校验内容哈希、文件头（magic bytes）与扩展名一致性；仅接受 png/jpg/gif/webp/avif，**不含 SVG**（同源存储型 XSS 风险）。

Web 服务器也可以通过环境变量配置 S3：`MEMODUMP_IMAGE_S3_ENDPOINT`、`_REGION`、`_BUCKET`、`_PREFIX`、`_PUBLIC_URL`、`_ACCESS_KEY`、`_SECRET_KEY`、`_FORCE_PATH_STYLE`（优先级高于设置面板，此时面板只读）。

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

云同步没有 CLI 表面：CLI Web 服务器不参与同步（见[云同步](#云同步实验性)）；Wails 桌面构建从操作系统应用数据目录读取其同步状态。

### 云同步（实验性）

云同步通过 S3 兼容存储桶让多个 MemoDump 安装的 Markdown 笔记保持同步。它是**实验性功能，默认关闭**；设置面板会显示明文/无端到端加密警告。它属于**最终一致**而非实时同步：A 设备的改动在 A 的下一次运行上传，在 B 的下一次运行下载，正常延迟约为两个周期。

**谁参与同步。** 云同步在仓库所在运行时内运行：

| 运行时 | 仓库 | 同步引擎 | 自动运行时长 |
|--------|------|---------|-------------|
| Wails 桌面 | 文件系统 | 已审查的 Go 引擎（R0–R5） | 应用打开期间 |
| 纯前端 / PWA（`VITE_LOCAL=1`） | IndexedDB | 浏览器引擎（R6） | 页面/PWA 打开期间 |
| CLI Web 服务器 | 服务器文件系统 | 无 | — |

CLI Web 服务器的浏览器客户端**不**参与同步：它们共享同一个服务器仓库，没有需要收敛的内容。两个同步引擎是**线协议兼容**的——相同的 `repo.json` + `notes/<sync-id>.json` 版本化笔记记录与相同测试夹具——因此 Wails 副本与 PWA 副本可以同步到同一个存储桶。

**提供方配置（Wails 桌面）。** 在每个安装上设置以下环境变量：

| 变量 | 含义 |
|------|------|
| `MEMODUMP_SYNC_ENDPOINT` | S3 兼容端点 URL（例如 `https://s3.region.amazonaws.com`）。仅 `localhost`/回环开发允许明文 HTTP，端点不能携带路径。 |
| `MEMODUMP_SYNC_BUCKET` | **私有**存储桶（绝不能公共读）。 |
| `MEMODUMP_SYNC_PREFIX` | 可选对象前缀（例如 `memo/vault-a`）。 |
| `MEMODUMP_SYNC_REGION` | 区域（默认 `us-east-1`）。 |
| `MEMODUMP_SYNC_ACCESS_KEY` / `MEMODUMP_SYNC_SECRET_KEY` | 凭据。它们绝不写入仓库或发送到前端；无秘密的提供方指纹只哈希 端点/存储桶/前缀。 |
| `MEMODUMP_SYNC_FORCE_PATH_STYLE` | 路径风格寻址（MinIO、R2、LocalStack）时设为 `1`。 |

所有笔记内容以**未加密**方式同步（无端到端加密）。请使用私有存储桶并限制其凭据；优先使用 HTTPS，除回环开发外拒绝明文 HTTP。

**浏览器构建（纯前端 / PWA）。** PWA 在浏览器 localStorage 中保存自己的 S3 配置（端点、区域、存储桶、前缀、访问/密钥、路径风格）——设置面板会显示明文凭据警告。存储桶必须允许来自应用来源的签名流量：CORS 必须允许 `PUT/GET/HEAD/DELETE` 方法与 `Authorization`、`Content-Type`、`x-amz-*`、`If-Match`、`If-None-Match` 请求头，并暴露 `ETag` 与 `Retry-After` 响应头（面板显示完整模板）。同步仅在页面或 PWA 打开期间运行——关闭即停止，之后不会在后台工作。PWA 的仓库、同步身份、快照与恢复副本存放在 IndexedDB 中：清除站点数据或使用无痕窗口会丢弃或隔离它们，届时该 PWA 下次启用时会作为全新副本接入同一仓库（本地的未同步修改与恢复副本会丢失）。

**运行行为。** 已连接的副本（点击过一次**启用**）在其运行时打开期间自动运行：**启动延迟 10 秒**后运行一次，之后每**五分钟**运行一次，并在成功启用后立即额外运行一次。设置面板中的 `Run now` 仍然可以强制立即运行。调度对 Wails 桌面（应用打开期间）与 PWA（页面打开期间）相同；运行时关闭后不再进行任何同步。瞬时提供方故障使用内存退避重试（`1m, 2m, 5m, 10m, 30m`，遵循提供方更大的 `Retry-After`，成功后重置；重启即遗忘）。鉴权/权限/配额/不匹配失败会让自动同步**暂停**直到运行时结束——状态会显示暂停原因——而 `Run now` 仍然可用；成功的手动运行或启用会解除暂停。

**云同步不是备份。** 删除会传播到所有设备。提供方侧的版本/历史是 MemoDump 之外的。应用在拉取删除前写入的持久化**恢复副本**只是本地安全辅助：可在设置面板中查看并恢复。

**Wails 状态目录。** Wails 桌面把同步设备状态——设备 ID 与 路径→副本 注册表、连接记录（提供方指纹 + 仓库 ID）、一份一次性快照、以及恢复副本——存放在操作系统应用数据目录中，位于**仓库之外**。它**不包含** WAL、游标或持久化调度队列，绝不会被同步或上传；仓库已连接时请勿删除，否则副本将保守地重新接入。

**请勿与其它文件系统同步工具混用。** 启用 MemoDump 云同步时，不要把同一仓库放进 Dropbox/iCloud/OneDrive、git 自动化或其它文件同步工具——两者会竞争同一批 Markdown 文件。

**连接管理。** 设置面板显示连接状态、最近一次（已脱敏的）运行、下次计划运行与恢复副本。**停用**会停止自动运行并保留你的身份，因此使用同一提供方重新启用即可干净重连。**重置并重连**（需确认）会丢弃本副本的快照与连接指纹，以便你刻意切换提供方或重建丢失的仓库。普通运行绝不会自行重建丢失的仓库。

**测试提供方。** 可选的 S3 实机测试使用随机隔离前缀并在结束后清理，绝不会打印凭据：

```sh
MEMODUMP_S3_LIVE_ENDPOINT=https://… \
MEMODUMP_S3_LIVE_BUCKET=… \
MEMODUMP_S3_LIVE_ACCESS=… \
MEMODUMP_S3_LIVE_SECRET=… \
go test ./internal/syncprovider/s3/ -run TestS3Live -v
```

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

数据卷挂载到镜像内的 `/data`（镜像内已设置 `MEMODUMP_DATA=/data`）。可用标签：`latest`、`vX.Y.Z`、`vX.Y`。所有 [CLI 环境变量](#配置来源) 在容器内同样适用。云同步不在容器内运行：镜像是无界面的 CLI 服务器，其浏览器客户端共享同一个服务器仓库（见[云同步](#云同步实验性)）。

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

### 纯前端 / PWA

无 Go 服务器的纯浏览器构建：笔记存于 IndexedDB，云同步在浏览器内运行（见[云同步](#云同步实验性)）。使用 `VITE_LOCAL=1` 构建并托管静态 `dist` 目录，或运行 `npm run dev:local` 本地开发服务器（Vite 直接托管，并将 `/api` 代理到本地服务器以提供非同步功能）：

```sh
cd frontend && VITE_LOCAL=1 npm run build   # 生产构建
cd frontend && npm run dev:local            # 热重载开发服务器
```

浏览器构建必须在**安全上下文**中运行：通过 HTTPS 托管（开发时可用 `http://localhost`）。Web Locks、`crypto.subtle` 与安全的 IndexedDB 都依赖它，第二台设备绝不能指向普通的局域网 HTTP 地址。

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
