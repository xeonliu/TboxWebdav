![](https://s2.loli.net/2025/02/25/4QbqRWm9yk7CU2L.png)

<p align="center">
  <img align="center" src="https://img.shields.io/github/license/xeonliu/TboxWebdav" />
  <img align="center" src="https://img.shields.io/github/forks/xeonliu/TboxWebdav" />
  <img align="center" src="https://img.shields.io/github/stars/xeonliu/TboxWebdav" />
  <img align="center" src="https://img.shields.io/github/v/release/xeonliu/TboxWebdav?include_prereleases" />
  <img align="center" src="https://img.shields.io/github/downloads/xeonliu/TboxWebdav/total" />
</p>

# TboxWebdav-Go

> 本项目修改自 `Teru` 的 [TboxWebdav](https://github.com/1357310795/TboxWebdav) (GPL 2.0)

本文档介绍 **Go 语言重写版本**的 TboxWebdav。Go 版本与原 ASP.NET Core 版本功能完全对等，同时提供：

- **无运行时依赖**：单一可执行文件，无需安装 .NET 运行时。
- **全平台支持**：提供 Windows、macOS、Linux、FreeBSD 的 amd64 / arm64 等多种架构预编译包。
- **更低资源占用**：启动速度更快，内存占用更小。

---

## 程序简介

程序对接了新交大云盘（腾讯 SMH）API 和 WebDAV / FTP 协议，用户可以通过 WebDAV 或 FTP 协议访问新交大云盘，借助 RaiDrive 等工具可将新交大云盘挂载为网络磁盘，与系统文件管理器深度整合，使用体验接近本地磁盘。

---

## 下载

在 [GitHub Releases](https://github.com/xeonliu/TboxWebdav/releases) 页面找到以 `tboxwebdav-` 开头的资产，根据操作系统和架构选择合适的包：

| 文件名 | 适用平台 |
|---|---|
| `tboxwebdav-windows-amd64.zip` | Windows 64 位（主流 PC） |
| `tboxwebdav-windows-arm64.zip` | Windows ARM 64 位 |
| `tboxwebdav-windows-386.zip` | Windows 32 位 |
| `tboxwebdav-darwin-amd64.tar.gz` | macOS Intel |
| `tboxwebdav-darwin-arm64.tar.gz` | macOS Apple Silicon（M 系列） |
| `tboxwebdav-linux-amd64.tar.gz` | Linux 64 位 |
| `tboxwebdav-linux-arm64.tar.gz` | Linux ARM 64 位（如树莓派 4） |
| `tboxwebdav-linux-arm.tar.gz` | Linux ARM 32 位 |
| `tboxwebdav-freebsd-amd64.tar.gz` | FreeBSD 64 位 |

> **Go 版本无需安装任何运行时**，解压后直接运行即可。

macOS / Linux 用户解压后需赋予可执行权限：

```bash
tar -xzf tboxwebdav-darwin-arm64.tar.gz
chmod +x tboxwebdav
./tboxwebdav --help
```

---

## 快速开始

### 方式一：直接运行（推荐，混合认证模式）

不带任何参数启动，程序默认监听 `localhost:65472`，使用混合认证（Mixed）：

```bash
./tboxwebdav
```

在 WebDAV 客户端（如 RaiDrive、macOS Finder、Windows 资源管理器）中填写：

- **地址**：`http://localhost:65472`
- **用户名**：任意（不校验）
- **密码**：填入你的 JAAuthCookie **或** UserToken（详见下文"获取认证凭证"）

### 方式二：匿名登录（仅限单人自用，注意数据安全！）

使用 JAAuthCookie：

```bash
./tboxwebdav --auth None -C 你的JAAuthCookie
```

使用 UserToken：

```bash
./tboxwebdav --auth None -T 你的UserToken
```

此模式下 WebDAV 客户端无需输入用户名/密码即可直接连接。

### 方式三：自定义用户名密码（多用户 / 固定密码）

推荐通过配置文件设置，详见"配置文件"一节：

```bash
./tboxwebdav -c config.yaml
```

---

## 获取认证凭证

### JAAuthCookie

1. 用浏览器访问任意需要 jAccount 认证的页面（如 [my.sjtu.edu.cn](https://my.sjtu.edu.cn)）并登录。
2. 打开 [https://jaccount.sjtu.edu.cn/jaccount/](https://jaccount.sjtu.edu.cn/jaccount/)。
3. 按 F12 打开开发者工具，切换到「应用程序 → 存储 → Cookie」，找到 `JAAuthCookie` 的值。

![](https://s2.loli.net/2025/02/25/jZwpTbMv7yBDzUC.png)

### UserToken

1. 登录新云盘 [https://pan.sjtu.edu.cn/](https://pan.sjtu.edu.cn/)。
2. 按 F12 打开开发者工具，切换到「应用程序 → 存储 → Cookie」，找到 `UserToken` 的值。

![](https://s2.loli.net/2025/02/25/HvkTw4xS5OhfYgI.png)

---

## 命令行参数参考

```
Usage:
  tboxwebdav [flags]

Flags:
  -c, --config <file>               指定 YAML 格式的配置文件。使用配置文件时，其他命令行参数全部无效。
  -p, --port <port>                 HTTP 服务监听端口。 [默认: 65472]
      --host <host>                 HTTP 服务监听主机名或 IP 地址。 [默认: localhost]
      --cachesize <bytes>           缓存空间大小（字节，建议不低于 10MB）。 [默认: 20971520]
      --auth <mode>                 WebDAV/FTP 认证方式：None | JaCookie | UserToken | Custom | Mixed
                                      None      — 匿名认证，需同时指定 --cookie 或 --token。
                                      JaCookie  — 以 JAAuthCookie 作为密码进行认证。
                                      UserToken — 以 UserToken 作为密码进行认证。
                                      Custom    — 自定义用户名/密码，需配合 --username/--password
                                                  或配置文件使用。
                                      Mixed     — 混合模式，同时支持 JaCookie、UserToken 和
                                                  Custom 三种方式（推荐）。 [默认: Mixed]
  -U, --username <username>         自定义认证用户名（仅 Custom/Mixed 模式有效）。
  -P, --password <password>         自定义认证密码（仅 Custom/Mixed 模式有效）。
  -C, --cookie <cookie>             JAAuthCookie 字符串。
  -T, --token <token>               新云盘 UserToken 字符串。
      --access <mode>               访问权限：Full | ReadOnly | NoDelete [默认: Full]
                                      Full      — 允许读写及删除。
                                      ReadOnly  — 只读，禁止写入和删除。
                                      NoDelete  — 允许读写，禁止删除。
      --log-level <level>           日志级别：debug | info | warn | error [默认: info]
      --ftp                         启用 FTP 服务器（与 WebDAV 服务同时运行）。
      --ftp-port <port>             FTP 服务监听端口。 [默认: 2121]
      --ftp-passive-host <ip>       FTP PASV 模式对外公布的 IP 地址（公网部署时需设置）。
      --ftp-passive-port-start <n>  FTP 被动模式端口范围起始值（0 表示随机）。
      --ftp-passive-port-end <n>    FTP 被动模式端口范围终止值。
  -h, --help                        显示帮助信息
```

---

## 配置文件参考

使用 `-c config.yaml` 指定配置文件时，所有命令行参数失效，以配置文件为准。

```yaml
Host: 0.0.0.0          # HTTP/FTP 服务监听主机，默认 localhost
Port: 65472            # WebDAV HTTP 服务监听端口，默认 65472
CacheSize: 20971520    # 目录缓存大小（字节），默认 20MB
AuthMode: Mixed        # 认证模式，默认 Mixed
AccessMode: Full       # 访问模式，默认 Full
Cookie: ""             # JAAuthCookie（可选）
UserToken: ""          # UserToken（可选）

# FTP 服务设置（可选）
FTPEnabled: true       # 是否启用 FTP 服务，默认 false
FTPPort: 2121          # FTP 服务监听端口，默认 2121
FTPPassiveHost: ""     # PASV 对外公布的 IP（公网部署时填写公网 IP）
FTPPassivePortStart: 50000  # PASV 端口范围起始（0 = 随机）
FTPPassivePortEnd:   51000  # PASV 端口范围终止

# 自定义用户列表（Custom / Mixed 模式下生效，WebDAV 和 FTP 共用）
Users:
  - UserName: alice
    Password: mypassword
    UserToken: 你的UserToken        # 该用户使用 UserToken 认证云盘
  - UserName: bob
    Password: bobpassword
    Cookie: 你的JAAuthCookie        # 该用户使用 JAAuthCookie 认证云盘
    AccessMode: ReadOnly            # 该用户仅有只读权限
```

> **说明**：`Users` 列表中每个用户可单独设置 `AccessMode`，未设置则继承全局 `AccessMode`。FTP 与 WebDAV 共享同一套认证配置，认证方式与密码格式完全相同。

---

## 自行构建

需要 Go 1.21 或更高版本。

```bash
# 克隆仓库
git clone https://github.com/xeonliu/TboxWebdav.git
cd TboxWebdav

# 下载依赖
go mod download

# 构建（当前平台）
go build -o tboxwebdav ./cmd/tboxwebdav/

# 交叉编译示例：构建 Windows amd64 版本
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o tboxwebdav.exe ./cmd/tboxwebdav/

# 交叉编译示例：构建 macOS Apple Silicon 版本
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o tboxwebdav ./cmd/tboxwebdav/
```

CI 构建由 [`.github/workflows/release_go.yml`](.github/workflows/release_go.yml) 自动完成，覆盖所有支持平台，并在发布 Release 时自动上传到 GitHub Release 页面。

---

## 与 ASP.NET Core 版本的差异

| 功能 | ASP.NET Core 版本 | Go 版本 |
|---|---|---|
| 运行时依赖 | 需要 .NET 8 Runtime | **无依赖** |
| 二进制体积 | 较大（含运行时约 60MB+） | 较小（约 3MB） |
| 支持平台 | Windows / Linux / macOS | Windows / Linux / macOS / FreeBSD / ARM |
| 认证模式 | None / JaCookie / UserToken / Custom / Mixed | 完全相同 |
| 配置文件 | YAML，字段名相同 | YAML，字段名相同 |
| 日志 | ASP.NET Core 内置日志 | 结构化日志（`log/slog`），支持 `--log-level` |
| 目录缓存 | 无 | **30 秒 TTL，变更时自动失效** |
| FTP 支持 | 无 | **支持，通过 `--ftp` 启用** |

---

## 说在最后

如果觉得程序好用的话，请点亮右上角的 Star 哦~

欢迎 Bug Report & Pull Request！
