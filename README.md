# StoreHub Auth CLI

`storehub-auth` 是 StoreHub 内部命令行登录工具。它通过 Keycloak、Google 登录和 OAuth 2.0 Authorization Code + PKCE 获取个人短期 Access Token，供 Grafana API、Grafana MCP 等本地工具使用。

认证链路：

```text
storehub-auth
  -> 浏览器 Google 登录
  -> Keycloak（用户身份、组和角色校验）
  -> ~/.storehub/token.txt
  -> Grafana MCP / curl
  -> Kong（校验 Keycloak JWT）
  -> Grafana API
```

## 功能

- 使用系统默认浏览器完成 Google / Keycloak 登录。
- Public Client，无需在开发者电脑保存 `client_secret`。
- 使用 PKCE S256 和随机 `state` 防止授权码被截获或回调伪造。
- Access Token 过期后，优先使用 Refresh Token 静默续期。
- 可使用 `-f` 或 `--force` 强制重新登录。
- Token 文件权限为 `0600`，配置目录权限为 `0700`。
- Grafana MCP 可直接读取 Token 文件，不需要把 Token 写死在 Codex 配置中。

## 使用前提

- macOS、Linux 或 Windows。
- Go 1.23.6 或更高版本（从源码构建时需要）。
- 能访问：
  - `https://keycloak.shub.us`
  - `https://grafana-api.mymyhub.com`
- Keycloak 账号已被授权使用 CLI。当前策略是只有具备 `storehub-auth-cli.grafana-api-access` Client Role 的用户可以完成 CLI 登录；该角色通常通过 `devops` 组继承。
- 本机端口 `127.0.0.1:8089` 未被其他程序占用。

## 安装

### 方式一：从源码构建并安装（推荐）

进入项目目录：

```bash
cd /path/to/storehub-auth
```

确认 Go 环境：

```bash
go version
```

运行测试并构建：

```bash
go test ./...
go build -o storehub-auth .
```

macOS / Linux 安装到个人命令目录：

```bash
mkdir -p ~/.local/bin
install -m 0755 storehub-auth ~/.local/bin/storehub-auth
```

把 `~/.local/bin` 加入 `PATH`（如果尚未配置）：

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

使用 Bash 时，将 `~/.zshrc` 替换为 `~/.bashrc`。

验证安装：

```bash
command -v storehub-auth
storehub-auth -h
```

### 方式二：直接从源码运行

适合开发和测试：

```bash
cd /path/to/storehub-auth
go run main.go
```

## 登录和续期

正常登录：

```bash
storehub-auth
```

执行逻辑：

1. 如果本地存在可用 Refresh Token，工具会静默刷新 Access Token。
2. 如果没有本地凭证、Refresh Token 已失效或续期失败，工具会打开默认浏览器。
3. Google / Keycloak 登录成功后，浏览器回调到 `http://127.0.0.1:8089/callback`。
4. 新 Token 会覆盖写入本地文件。
5. 成功页面会在 1.5 秒后尝试自动关闭当前标签页；如果浏览器阻止自动关闭，可手动关闭，不影响登录结果。

强制忽略本地会话并重新打开浏览器：

```bash
storehub-auth --force
# 简写
storehub-auth -f
```

本地文件：

| 路径 | 内容 | 权限 |
| --- | --- | --- |
| `~/.storehub/token.txt` | 当前 Access Token | `0600` |
| `~/.storehub/credentials.json` | Refresh Token 和 Access Token 到期时间 | `0600` |
| `~/.storehub/` | 凭证目录 | `0700` |

不要提交、复制或发送这些文件。Access Token 是短期个人凭证，Refresh Token 的有效窗口由 Keycloak Client Session 策略控制。

## 验证 Grafana API

登录成功后执行：

```bash
curl -i \
  -H "Authorization: Bearer $(cat ~/.storehub/token.txt)" \
  https://grafana-api.mymyhub.com/api/dashboards/home
```

预期返回 `HTTP 200`。如果返回 `401`，先重新运行：

```bash
storehub-auth
```

## 配置 Grafana MCP（Codex）

### 1. 安装 `uv`

如果本机还没有 `uvx`，先安装 `uv`。macOS 可使用：

```bash
brew install uv
```

验证：

```bash
uvx --version
```

### 2. 配置 MCP

在 `~/.codex/config.toml` 中添加：

```toml
[mcp_servers.grafana]
enabled = true
command = "uvx"
args = ["mcp-grafana", "--disable-write"]

[mcp_servers.grafana.env]
GRAFANA_URL = "https://grafana-api.mymyhub.com"
GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE = "/Users/<你的用户名>/.storehub/token.txt"
UV_CACHE_DIR = "/private/tmp/uv-cache-grafana"
```

注意：

- 虽然变量名叫 `GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`，这里保存的是个人 Keycloak Access Token；MCP 只负责读取并放入 `Authorization: Bearer` 请求头，Kong 会先校验个人 Token，再在服务端访问 Grafana。
- `mcp-grafana` 会在每次请求时重新读取 Token 文件。运行 `storehub-auth` 刷新 Token 后，不需要修改 MCP 配置。
- `--disable-write` 会禁用 MCP 写操作，建议默认保留。确实需要修改 Dashboard 等资源时，应经过权限评审后再移除。
- MCP 工具通常在 Codex 任务启动时注册。修改配置后，请新建 Codex 任务或重启 Codex。

### 3. 使用顺序

```bash
# 获取或刷新个人 Token
storehub-auth

# 然后启动/重启 Codex，在任务中使用 Grafana MCP
```

## 退出登录和清理凭证

删除本地凭证：

```bash
rm ~/.storehub/token.txt ~/.storehub/credentials.json
```

下次执行 `storehub-auth` 时会重新打开浏览器登录。

如果怀疑凭证泄露，除了删除本地文件，还应在 Keycloak 中注销用户会话；仅删除本地文件不会使已经签发的 Access Token 立即失效，它仍可能在到期前有效。

## 常见问题

### `listen tcp 127.0.0.1:8089: bind: address already in use`

端口 `8089` 已被占用。查找占用进程：

```bash
lsof -nP -iTCP:8089 -sTCP:LISTEN
```

结束确认无用的进程后重新登录。不要在 Keycloak 未同步修改 Redirect URI 的情况下随意更换端口。

### 浏览器显示没有权限访问 Grafana API

这表示 Keycloak 已成功识别用户，但该用户没有 `storehub-auth-cli.grafana-api-access` 角色。请联系 DevOps 检查用户是否属于被授权组。

### Grafana API 或 MCP 返回 `401`

依次检查：

```bash
storehub-auth

curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $(cat ~/.storehub/token.txt)" \
  https://grafana-api.mymyhub.com/api/dashboards/home
```

如果重新登录后仍不是 `200`，请检查系统时间、Keycloak 会话状态以及 Kong JWT 配置。

### 登录成功，但 Codex 中没有 Grafana MCP 工具

MCP 列表不会在当前任务中热加载。确认 `~/.codex/config.toml` 配置正确后，新建一个 Codex 任务或重启 Codex。

## 管理员接入要求

普通用户不需要执行本节。部署新环境时应至少具备以下配置：

### Keycloak Client

```text
Client ID: storehub-auth-cli
Client authentication: OFF
Standard flow: ON
PKCE method: S256
Valid redirect URI: http://127.0.0.1:8089/callback
```

建议同时配置：

- Access Token Audience：`grafana-api`
- Groups mapper：在 Access Token 中加入 `groups`
- Client Role：`grafana-api-access`
- Client Scope 标记：`storehub-auth-cli-access-check`，仅作为 `storehub-auth-cli` 的 Default Client Scope
- 将 `grafana-api-access` 分配给允许使用 CLI 的组，例如 `devops`
- Google Identity Provider Post Login Flow：仅当请求包含上述 Client Scope 且用户缺少上述 Client Role 时执行 `Deny access`
- 为 `storehub-auth-cli` 单独设置 Access Token Lifespan、Client Session Idle 和 Client Session Max

### 获取 Keycloak Realm 签名公钥

Kong 使用 Keycloak Realm 的 RSA 公钥验证 Access Token 签名。这里需要的是**签名公钥**，不是 Keycloak 网站的 HTTPS/TLS 证书，也绝不能导出 Realm 私钥。

#### 方式一：从 Keycloak 管理后台获取

1. 登录 Keycloak Admin Console，选择 `StoreHub` Realm。
2. 打开 `Realm settings` → `Keys`。
3. 在 Active Keys 中找到：
   - Status：`Active`
   - Use：`SIG` / Signing
   - Algorithm：`RS256`
4. 记录该 Key 的 `kid`。
5. 打开该 Key 的 Certificate / Public key，复制公钥。
6. 保存为 `keycloak-public.pem`，内容应类似：

```pem
-----BEGIN PUBLIC KEY-----
...
-----END PUBLIC KEY-----
```

不同 Keycloak 版本的按钮名称可能略有不同。如果后台提供的是 `BEGIN CERTIFICATE`，可转换为 Kong 所需的公钥：

```bash
openssl x509 \
  -in keycloak-signing.crt \
  -pubkey -noout \
  > keycloak-public.pem
```

验证 PEM 文件可以被 OpenSSL 解析：

```bash
openssl pkey \
  -pubin \
  -in keycloak-public.pem \
  -noout -text
```

#### 方式二：从 Realm JWKS Endpoint 获取

Keycloak 对外公开 Realm 验签证书：

```text
https://keycloak.shub.us/realms/StoreHub/protocol/openid-connect/certs
```

先查看所有可用 Key：

```bash
curl -fsS \
  https://keycloak.shub.us/realms/StoreHub/protocol/openid-connect/certs \
  | jq '.keys[] | {kid, use, alg, kty}'
```

Realm 可能同时保留 Active 和 Passive Key，因此不要盲目选择返回结果的第一项。使用 Keycloak 后台显示的 Active RS256 `kid`，或者从一个刚签发的 Access Token Header 中确认 `kid`。

设置当前 Active Key ID：

```bash
KID='<active-rs256-kid>'
```

从 JWKS 中提取对应的 X.509 证书：

```bash
curl -fsS \
  https://keycloak.shub.us/realms/StoreHub/protocol/openid-connect/certs \
  | jq -r --arg kid "$KID" \
      '.keys[] | select(.kid == $kid and .use == "sig" and .alg == "RS256") | .x5c[0]' \
  | fold -w 64 \
  > keycloak-signing.der.b64

{
  echo '-----BEGIN CERTIFICATE-----'
  cat keycloak-signing.der.b64
  echo '-----END CERTIFICATE-----'
} > keycloak-signing.crt
```

确认没有选错 Key，然后转成 Kong 使用的 RSA 公钥：

```bash
openssl x509 \
  -in keycloak-signing.crt \
  -noout -subject -issuer -fingerprint

openssl x509 \
  -in keycloak-signing.crt \
  -pubkey -noout \
  > keycloak-public.pem

openssl pkey \
  -pubin \
  -in keycloak-public.pem \
  -noout -text
```

如果 `keycloak-signing.der.b64` 为空，说明 `KID` 不匹配、该 Key 不是 RS256 Signing Key，或者 JWKS 没有提供 `x5c` 字段；此时不要继续更新 Kong。

#### 配置到 Kong JWT Credential

为 Keycloak Consumer 创建 JWT Credential：

```text
key: storehub-auth-cli
algorithm: RS256
rsa_public_key: <keycloak-public.pem 的完整内容>
secret: 留空
```

对应的 Kong JWT Plugin 至少应配置：

```text
key claim name: azp
claims to verify: exp
header names: authorization
```

`storehub-auth-cli` 必须与 Access Token 中的 `azp` 一致。更新后使用以下请求验证：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $(cat ~/.storehub/token.txt)" \
  https://grafana-api.mymyhub.com/api/dashboards/home
```

预期合法 Token 返回 `200`；缺少 Token、过期 Token、错误签名或错误 `azp` 应返回 `401`。

#### 公钥轮换

- Realm 公钥不是秘密，可以提供给 Kong 和其他验签服务。
- Realm 私钥必须始终留在 Keycloak 管理的 KeyStore / Secret 中。
- Token 有效期和 Realm 签名密钥有效期是两套独立机制；Access Token 到期不会自动更换 Realm Key。
- 如果 Kong 使用静态 PEM，Keycloak 切换 Active Signing Key 后，必须同步更新 Kong，否则新签发 Token 会返回 `401`。
- 轮换时先将新公钥部署到所有验签端，再切换 Keycloak Active Key；旧公钥至少保留到所有旧 Token 过期。
- 每次变更后都要核对新 Token Header 的 `kid`、JWKS 中的 `kid` 和 Kong 当前公钥是否一致。

### Kong / Grafana

- Kong JWT 插件使用 Keycloak Realm 公钥验证 RS256 签名。
- JWT Credential 使用 `azp=storehub-auth-cli` 匹配该客户端。
- 必须验证 `exp`，并拒绝无 Token、过期 Token、错误签名和错误 Client 的请求。
- Kong 验证通过后，在服务端替换为 Grafana Service Account Token，再转发到真实 Grafana API。
- Grafana Service Account Token 只保存在 Kong/Kubernetes Secret 等服务端密钥存储中，不分发给开发者。
- Grafana 网页登录继续使用原有 `grafana` Generic OAuth Client，不应受 CLI Post Login 策略影响。

## 安全说明

- Keycloak Realm 公钥用于验签，不属于秘密；Realm 私钥必须只由 Keycloak 管理，不能分发到客户端或 Kong。
- 不要将 Token 放入 Git、Shell 历史、聊天消息、日志或截图。
- 不要绕过 `https://grafana-api.mymyhub.com` 直接访问真实 Grafana API 地址。
- 人员离职或权限变更时，应撤销 Keycloak 组/角色并注销现有会话。已经签发的 Access Token 最迟在其 `exp` 到期后失效。
- 建议管理员定期轮换 Grafana Service Account Token；轮换不会要求所有开发者重新登录。

## 当前内置配置

| 配置 | 值 |
| --- | --- |
| Keycloak Realm | `StoreHub` |
| Keycloak Client | `storehub-auth-cli` |
| OAuth Flow | Authorization Code + PKCE S256 |
| Callback | `http://127.0.0.1:8089/callback` |
| Scope | `openid profile email` |
| Grafana API Gateway | `https://grafana-api.mymyhub.com` |
