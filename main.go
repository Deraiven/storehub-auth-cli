package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ==================== 核心配置 ====================
const (
	KeycloakURL = "https://keycloak.shub.us/realms/StoreHub/protocol/openid-connect"
	ClientID    = "storehub-auth-cli"
	Port        = "8089"
	RedirectURI = "http://127.0.0.1:" + Port + "/callback"
)

var (
	configDir       string
	tokenFile       string
	credentialsFile string
)

type Credentials struct {
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("❌ 无法获取用户主目录: %v\n", err)
		os.Exit(1)
	}
	configDir = filepath.Join(home, ".storehub")
	tokenFile = filepath.Join(configDir, "token.txt")
	credentialsFile = filepath.Join(configDir, "credentials.json")
}

// ==================== 工具函数 ====================
func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // linux
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

func randomURLSafe(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func saveTokens(accessToken, refreshToken string, expiresIn int64) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return err
	}

	// 1. 写入纯文本 Token
	if err := os.WriteFile(tokenFile, []byte(accessToken), 0600); err != nil {
		return err
	}
	if err := os.Chmod(tokenFile, 0600); err != nil {
		return err
	}

	// 2. 写入 JSON 凭证供静默刷新
	creds := Credentials{
		RefreshToken: refreshToken,
		ExpiresAt:    time.Now().Unix() + expiresIn,
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(credentialsFile, data, 0600); err != nil {
		return err
	}
	return os.Chmod(credentialsFile, 0600)
}

// ==================== OAuth2 逻辑 ====================
func tryRefreshToken() bool {
	if _, err := os.Stat(credentialsFile); os.IsNotExist(err) {
		return false
	}

	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return false
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return false
	}

	if creds.RefreshToken == "" {
		return false
	}

	// 构造向 Keycloak 刷新的 x-www-form-urlencoded 请求
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {ClientID},
	}

	resp, err := client.PostForm(KeycloakURL+"/token", form)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	accToken, _ := result["access_token"].(string)
	refToken, _ := result["refresh_token"].(string)
	expInRaw, _ := result["expires_in"].(float64)
	if refToken == "" {
		refToken = creds.RefreshToken
	}

	if accToken != "" {
		_ = saveTokens(accToken, refToken, int64(expInRaw))
		return true
	}
	return false
}

func performBrowserLogin() {
	state, err := randomURLSafe(32)
	if err != nil {
		fmt.Printf("❌ 无法生成 OAuth state: %v\n", err)
		os.Exit(1)
	}
	codeVerifier, err := randomURLSafe(64)
	if err != nil {
		fmt.Printf("❌ 无法生成 PKCE verifier: %v\n", err)
		os.Exit(1)
	}

	type callbackResult struct {
		code string
		err  error
	}
	resultCh := make(chan callbackResult, 1)

	// 启动本地临时 Callback 服务
	server := &http.Server{Addr: "127.0.0.1:" + Port}
	mux := http.NewServeMux()
	server.Handler = mux

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			resultCh <- callbackResult{err: fmt.Errorf("Keycloak 拒绝授权: %s", oauthErr)}
			http.Error(w, "授权失败，可以关闭此窗口。", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("state") != state {
			resultCh <- callbackResult{err: fmt.Errorf("OAuth state 不匹配")}
			http.Error(w, "无效的登录回调，可以关闭此窗口。", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `
				<html>
				<body style="font-family: sans-serif; text-align: center; padding-top: 50px; background: #1e1e24; color: #fff;">
					<h2 style="color: #4caf50;">🔓 StoreHub 认证成功！</h2>
					<p>临时安全 Token 已成功写入本地，MCP / Grafana 替换就绪。</p>
					<p id="close-hint" style="color: #888; font-size: 13px;">当前页面将在 1.5 秒后自动关闭。</p>
					<script>
						setTimeout(function () {
							window.open('', '_self');
							window.close();
							setTimeout(function () {
								document.getElementById('close-hint').textContent =
									'浏览器阻止了自动关闭，您可以关闭此页面并返回终端。';
							}, 300);
						}, 1500);
					</script>
				</body>
				</html>
			`)
			resultCh <- callbackResult{code: code}
		} else {
			resultCh <- callbackResult{err: fmt.Errorf("回调中缺少授权码")}
			http.Error(w, "回调中缺少授权码，可以关闭此窗口。", http.StatusBadRequest)
		}
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("❌ 本地服务启动失败: %v\n", err)
			os.Exit(1)
		}
	}()

	authQuery := url.Values{
		"client_id":             {ClientID},
		"redirect_uri":          {RedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(codeVerifier)},
		"code_challenge_method": {"S256"},
	}
	authURL := KeycloakURL + "/auth?" + authQuery.Encode()
	fmt.Println("🌐 正在唤起默认浏览器进行 Google 统一身份认证...")
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("无法自动打开浏览器，请手动复制此链接打开:\n%s\n", authURL)
	}

	// 等待回调拿到授权码
	callback := <-resultCh
	_ = server.Shutdown(context.Background())
	if callback.err != nil {
		fmt.Printf("❌ 登录失败: %v\n", callback.err)
		os.Exit(1)
	}

	fmt.Println("🔑 已获取授权码，正在向 Keycloak 换取凭证...")
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {callback.code},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {codeVerifier},
	}

	resp, err := client.PostForm(KeycloakURL+"/token", form)
	if err != nil {
		fmt.Printf("❌ 请求 Token 失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		fmt.Printf("❌ 换取 Token 失败（HTTP %d）: %s\n", resp.StatusCode, body)
		os.Exit(1)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("❌ 无法解析 Token 响应: %v\n", err)
		os.Exit(1)
	}

	accToken, _ := result["access_token"].(string)
	refToken, _ := result["refresh_token"].(string)
	expInRaw, _ := result["expires_in"].(float64)

	if accToken == "" {
		fmt.Println("❌ Keycloak 响应中没有 access_token")
		os.Exit(1)
	}
	if saveTokens(accToken, refToken, int64(expInRaw)) == nil {
		fmt.Printf("🎉 登录成功！临时 Token 已写入/替换: %s\n", tokenFile)
	} else {
		fmt.Println("❌ Token 写入本地文件失败！")
		os.Exit(1)
	}
}

// ==================== 主入口 ====================
func main() {
	force := flag.Bool("force", false, "强制弹窗重新登录，忽略本地缓存")
	flag.BoolVar(force, "f", false, "强制弹窗重新登录，忽略本地缓存")
	flag.Parse()

	if *force {
		performBrowserLogin()
		return
	}

	fmt.Println("🔍 正在检查本地凭证状态...")
	if tryRefreshToken() {
		fmt.Printf("✨ 检测到有效会话，已在后台完成自动续期！\n👉 Token 已更新替换至: %s\n", tokenFile)
	} else {
		fmt.Println("ℹ️ 本地无有效凭证或已过期，需要重新授信。")
		performBrowserLogin()
	}
}
