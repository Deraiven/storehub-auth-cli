# StoreHub Auth CLI 开发者安装与 Grafana MCP 使用文档

本文面向需要在本机使用 Codex / Grafana MCP 查询 Grafana 的开发者。

这份文档只说明客户端安装、登录和本地 Token 自动刷新方式。

## 1. 使用场景

`storehub-auth` 用于在本机完成公司统一登录，并把短期个人 Access Token 写入本地文件，供 Grafana MCP 或本地脚本读取。

简化链路：

```text
开发者运行 storehub-auth
  -> 浏览器完成公司账号登录
  -> 本地写入 ~/.storehub/token.txt
  -> Grafana MCP 读取 token.txt
  -> 调用 Grafana API 网关
```

开发者电脑里只保存个人登录产生的本地凭证，不需要配置任何客户端密码。

## 2. 使用前提

使用前请确认：

- 你使用的是公司账号。
- 你已经被授权使用 Grafana API。
- 本机可以访问：
  - `https://keycloak.shub.us`
  - `https://grafana-api.mymyhub.com`
- 本机 `127.0.0.1:8089` 端口没有被其他程序占用。

未授权用户登录时会看到类似提示：

```text
This account is not authorized to access the Grafana API
```

遇到这个提示时，请联系团队负责人确认权限。

## 3. 安装 StoreHub Auth CLI

Release 页面：

```text
https://github.com/Deraiven/storehub-auth-cli/releases/latest
```

### 3.1 macOS Apple Silicon

适用于 M1、M2、M3、M4 及后续 Apple Silicon 芯片。

```bash
mkdir -p ~/.local/bin

curl -fL \
  https://github.com/Deraiven/storehub-auth-cli/releases/latest/download/storehub-auth-darwin-arm64 \
  -o ~/.local/bin/storehub-auth

chmod 0755 ~/.local/bin/storehub-auth
```

如果 macOS 第一次运行时提示无法打开，请先确认下载来源，然后执行：

```bash
xattr -d com.apple.quarantine ~/.local/bin/storehub-auth
```

### 3.2 Linux x86_64 / amd64

当前 Linux Release 是 amd64 二进制，不适用于 ARM64 Linux。

```bash
mkdir -p ~/.local/bin

curl -fL \
  https://github.com/Deraiven/storehub-auth-cli/releases/latest/download/storehub-auth-linux-amd64 \
  -o ~/.local/bin/storehub-auth

chmod 0755 ~/.local/bin/storehub-auth
```

### 3.3 配置 PATH

Zsh：

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Bash：

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

验证安装：

```bash
command -v storehub-auth
storehub-auth -h
```

## 4. 首次登录

执行：

```bash
storehub-auth
```

工具会：

1. 在 `127.0.0.1:8089` 启动临时回调服务。
2. 打开默认浏览器进入公司账号登录。
3. 登录成功后写入本地凭证文件。
4. 浏览器成功页会尝试自动关闭当前登录标签页。

本地文件：

| 路径 | 用途 | 权限 |
| --- | --- | --- |
| `~/.storehub/` | 凭证目录 | `0700` |
| `~/.storehub/token.txt` | 当前 Access Token | `0600` |
| `~/.storehub/credentials.json` | 刷新登录状态所需的本地凭证 | `0600` |

强制重新打开浏览器登录：

```bash
storehub-auth --force
```

也可以使用简写：

```bash
storehub-auth -f
```

## 5. 安装 Grafana MCP 运行环境

Grafana MCP 通过 `uvx` 启动。

macOS：

```bash
brew install uv
uvx --version
```

Linux：

请按照公司 Linux 软件安装规范安装 `uv`，然后确认：

```bash
uvx --version
```

不建议使用 root 用户启动 MCP，也不要把个人 Token 写入系统级环境变量。

## 6. 推荐方案：MCP 启动前自动刷新 Token

不要让 Codex 直接执行 `uvx mcp-grafana`，而是执行一个包装脚本。

工作方式：

```text
Codex 启动 Grafana MCP
  -> 包装脚本先运行 storehub-auth
  -> 获取本地刷新锁
  -> Refresh Token 可用：静默刷新 Access Token
  -> 静默刷新失败但 Access Token 仍未过期：继续复用
  -> 没有可用 Token：打开浏览器重新登录
  -> Token 写入成功
  -> 启动 mcp-grafana
```

这样每次 Codex 创建 Grafana MCP 进程时，都会优先通过 Refresh Token 静默刷新 Access Token；只有本地 Token 不可用时才会打开浏览器。

### 6.1 创建启动包装器

创建 `~/.local/bin/storehub-grafana-mcp`：

```bash
mkdir -p ~/.local/bin

cat > ~/.local/bin/storehub-grafana-mcp <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

AUTH_BIN="${STOREHUB_AUTH_BIN:-$HOME/.local/bin/storehub-auth}"

if [[ ! -x "$AUTH_BIN" ]]; then
  echo "storehub-auth not found or not executable: $AUTH_BIN" >&2
  exit 1
fi

if ! command -v uvx >/dev/null 2>&1; then
  echo "uvx is not installed or not in PATH" >&2
  exit 1
fi

# Ensure the local user token is usable before starting MCP.
# Keep status output on stderr so MCP stdout stays clean for JSON-RPC.
"$AUTH_BIN" >&2

exec uvx mcp-grafana --disable-write
EOF

chmod 0755 ~/.local/bin/storehub-grafana-mcp
```

注意：MCP 使用 stdio 与 Codex 通信。包装脚本必须把登录状态输出到 `stderr`，不要写到 `stdout`。

### 6.2 配置 Codex

编辑：

```text
~/.codex/config.toml
```

加入：

```toml
[mcp_servers.grafana]
enabled = true
command = "/Users/<你的用户名>/.local/bin/storehub-grafana-mcp"
args = []

[mcp_servers.grafana.env]
GRAFANA_URL = "https://grafana-api.mymyhub.com"
GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE = "/Users/<你的用户名>/.storehub/token.txt"
UV_CACHE_DIR = "/tmp/uv-cache-grafana"
```

Linux 用户将 `/Users/<你的用户名>` 改为实际 Home 路径，例如：

```text
/home/<你的用户名>
```

说明：`GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE` 是 `mcp-grafana` 使用的变量名；这里填写的是你的本地个人 Token 文件路径。

### 6.3 重新加载 MCP

修改配置后：

1. 关闭当前 Codex 任务。
2. 新建任务或重启 Codex。
3. 第一次使用 Grafana MCP 时，包装器会先检查或刷新 Token。

## 7. Token 自动刷新边界

需要理解几个边界：

- 包装器会在 Grafana MCP 进程启动之前优先尝试静默刷新 Token。
- `storehub-auth` 会使用本地刷新锁，避免多个进程同时刷新同一个 Refresh Token。

## 8. macOS 每小时自动刷新

从本仓库源码安装（需要 Go 1.23.6+）：

```bash
bash packaging/install-macos.sh
```

安装器会构建当前源码，根据当前用户 Home 生成 LaunchAgent，并注册每 3600 秒执行一次的任务。仓库中的 plist 是模板，不能直接复制加载。已发布的旧 Release 不一定包含 `--refresh-only`，本次更新请从源码安装。

对应 Keycloak 配置：Access Token Lifespan 为 2 小时，Client Session Idle 为 7 天，Client Session Max 为 30 天；Realm SSO 会话上限也需允许这些时长。刷新不会延长 30 天绝对上限。超过空闲期或最大寿命后需要重新浏览器登录。电脑休眠、关机或断网时不能保证按小时刷新，恢复使用时可先运行 `storehub-auth`。

角色撤销不会重新触发 Google Post Login Flow；网关需要检查每次请求的角色，离职时还需禁用账号并注销会话。已有 Access Token 的本地验签有效性以 `exp` 为准。

`storehub-auth --refresh-only` 只使用 Refresh Token 静默续期，不会打开浏览器，适合 `launchd` 等后台任务：

```bash
storehub-auth --refresh-only
```

刷新失败时命令返回非零状态，并提示在终端执行：

```bash
storehub-auth --force
```

本机安装的 LaunchAgent 名称为：

```text
com.storehub.auth-refresh
```

常用检查命令：

```bash
launchctl print gui/$(id -u)/com.storehub.auth-refresh
tail -n 50 ~/.storehub/refresh.log
tail -n 50 ~/.storehub/refresh.error.log
```

手动触发一次：

```bash
launchctl kickstart -k gui/$(id -u)/com.storehub.auth-refresh
```

卸载定时任务：

```bash
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.storehub.auth-refresh.plist
rm ~/Library/LaunchAgents/com.storehub.auth-refresh.plist
```
- 只要 Keycloak 接受本地 Refresh Token，`storehub-auth` 就会静默换取新的 Access Token。
- 如果静默刷新失败但 Access Token 仍未过期，`storehub-auth` 会继续复用现有 Token，不打开浏览器。
- `mcp-grafana` 会在每次请求时重新读取 `~/.storehub/token.txt`。
- 如果 MCP 已经运行很久，Token 在任务中途到期，可以在另一个终端执行：

```bash
storehub-auth
```

Token 文件更新后，MCP 下一次请求会自动使用新 Token，通常不需要重启 Codex。

- 如果本地登录状态超过最大会话时间，工具会重新打开浏览器登录。
- 该方案不会实现永久免登录。
- 如果某次 MCP 请求返回 `401`，先运行 `storehub-auth`，然后重试原来的 MCP 操作。

## 9. 验证

确认 Token 文件存在：

```bash
ls -l ~/.storehub/token.txt
```

确认 Grafana API 可访问：

```bash
curl -i \
  -H "Authorization: Bearer $(cat ~/.storehub/token.txt)" \
  https://grafana-api.mymyhub.com/api/dashboards/home
```

预期返回 `HTTP/2 200` 或 `HTTP/1.1 200`。

确认 MCP 包装器可启动：

```bash
~/.local/bin/storehub-grafana-mcp
```

这个命令会进入 MCP stdio 模式，看起来像“卡住”是正常的。按 `Ctrl+C` 退出即可。

## 10. 常见问题

### 登录后浏览器页面没有自动关闭

浏览器可能会阻止脚本关闭不是由脚本打开的标签页。只要页面显示认证成功，手动关闭即可，不影响登录结果。

### 提示 127.0.0.1:8089 端口被占用

查看占用进程：

```bash
lsof -nP -iTCP:8089 -sTCP:LISTEN
```

确认进程无用后再停止它。

### MCP 返回 401

先刷新本地 Token：

```bash
storehub-auth
```

然后重试 Codex 中的 Grafana MCP 操作。

如果仍然失败，请确认：

- 本机时间是否准确。
- 是否已经超过最大登录会话时间。
- 当前账号是否仍有 Grafana API 访问权限。

### uvx 找不到

确认：

```bash
command -v uvx
echo "$PATH"
```

如果 `uvx` 不在 Codex 启动时的 PATH 中，可以在包装器里使用 `uvx` 的绝对路径。

### 想清理本地登录状态

```bash
rm -f ~/.storehub/token.txt ~/.storehub/credentials.json
```

下次运行 `storehub-auth` 会重新打开浏览器登录。

## 11. 安全注意事项

- 不要把 `~/.storehub/token.txt` 或 `~/.storehub/credentials.json` 提交到 Git。
- 不要把 Token 复制到聊天、工单、截图或日志中。
- 不要在 `~/.codex/config.toml` 中直接粘贴 Token 内容，只配置 Token 文件路径。
- 离职、转岗或权限变化后，访问权限会由公司统一登录侧控制；本地工具不需要重新安装。

## 12. 快速命令清单

macOS Apple Silicon 安装：

```bash
mkdir -p ~/.local/bin
curl -fL https://github.com/Deraiven/storehub-auth-cli/releases/latest/download/storehub-auth-darwin-arm64 -o ~/.local/bin/storehub-auth
chmod 0755 ~/.local/bin/storehub-auth
```

Linux amd64 安装：

```bash
mkdir -p ~/.local/bin
curl -fL https://github.com/Deraiven/storehub-auth-cli/releases/latest/download/storehub-auth-linux-amd64 -o ~/.local/bin/storehub-auth
chmod 0755 ~/.local/bin/storehub-auth
```

登录或刷新：

```bash
storehub-auth
```

强制重新登录：

```bash
storehub-auth --force
```

验证 Grafana API：

```bash
curl -i -H "Authorization: Bearer $(cat ~/.storehub/token.txt)" https://grafana-api.mymyhub.com/api/dashboards/home
```
