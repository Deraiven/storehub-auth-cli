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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ==================== 核心配置 ====================
const (
	KeycloakURL = "https://keycloak.shub.us/realms/StoreHub/protocol/openid-connect"
	ClientID    = "storehub-auth-cli"
	Port        = "8089"
	RedirectURI = "http://127.0.0.1:" + Port + "/callback"

	refreshLockWait       = 30 * time.Second
	refreshLockStaleAfter = 2 * time.Minute
)

var (
	configDir       string
	tokenFile       string
	credentialsFile string
	refreshLockFile string
	tokenEndpoint   = KeycloakURL + "/token"
)

type Credentials struct {
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type tokenClaims struct {
	Exp int64 `json:"exp"`
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
	refreshLockFile = filepath.Join(configDir, "refresh.lock")
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

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func saveTokens(accessToken, refreshToken string, expiresIn int64) error {
	if accessToken == "" || refreshToken == "" || expiresIn <= 0 {
		return fmt.Errorf("invalid token response")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return err
	}

	// Persist rotated refresh credentials first, then atomically publish the access token.
	creds := Credentials{
		RefreshToken: refreshToken,
		ExpiresAt:    time.Now().Unix() + expiresIn,
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(credentialsFile, data); err != nil {
		return err
	}
	return atomicWrite(tokenFile, []byte(accessToken))
}

func accessTokenExpiresAt() (time.Time, error) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return time.Time{}, err
	}

	parts := strings.Split(strings.TrimSpace(string(data)), ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("access token is not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}

	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("access token has no exp claim")
	}

	return time.Unix(claims.Exp, 0), nil
}

func accessTokenHasAtLeast(remaining time.Duration) (time.Time, bool) {
	expiresAt, err := accessTokenExpiresAt()
	if err != nil {
		return time.Time{}, false
	}
	return expiresAt, time.Until(expiresAt) > remaining
}

func acquireRefreshLock() (func(), error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(refreshLockWait)
	for {
		f, err := os.OpenFile(refreshLockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(refreshLockFile) }, nil
		}

		if !os.IsExist(err) {
			return nil, err
		}

		if info, statErr := os.Stat(refreshLockFile); statErr == nil && time.Since(info.ModTime()) > refreshLockStaleAfter {
			_ = os.Remove(refreshLockFile)
			continue
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待刷新锁超时: %s", refreshLockFile)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// ==================== OAuth2 逻辑 ====================
func tryRefreshToken() (bool, string) {
	if _, err := os.Stat(credentialsFile); os.IsNotExist(err) {
		return false, "本地刷新凭证不存在"
	}

	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return false, fmt.Sprintf("读取本地刷新凭证失败: %v", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return false, fmt.Sprintf("解析本地刷新凭证失败: %v", err)
	}

	if creds.RefreshToken == "" {
		return false, "本地 refresh_token 为空"
	}

	// 构造向 Keycloak 刷新的 x-www-form-urlencoded 请求
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {ClientID},
	}

	resp, err := client.PostForm(tokenEndpoint, form)
	if err != nil {
		return false, fmt.Sprintf("请求 Keycloak 刷新失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Sprintf("Keycloak 拒绝刷新（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Sprintf("解析刷新响应失败: %v", err)
	}

	accToken, _ := result["access_token"].(string)
	refToken, _ := result["refresh_token"].(string)
	expInRaw, _ := result["expires_in"].(float64)
	if refToken == "" {
		refToken = creds.RefreshToken
	}

	if accToken != "" {
		if err := saveTokens(accToken, refToken, int64(expInRaw)); err != nil {
			return false, fmt.Sprintf("写入刷新后的 Token 失败: %v", err)
		}
		return true, ""
	}
	return false, "Keycloak 刷新响应中没有 access_token"
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
	completed := make(chan struct{}, 1)
	var callbackOnce sync.Once

	// 启动本地临时 Callback 服务
	server := &http.Server{Addr: "127.0.0.1:" + Port, ReadHeaderTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	server.Handler = mux

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.URL.Query().Get("state") != state {
			http.Error(w, "无效的登录回调", http.StatusBadRequest)
			return
		}
		accepted := false
		callbackOnce.Do(func() { accepted = true })
		if !accepted {
			http.Error(w, "回调已处理", http.StatusConflict)
			return
		}
		defer func() { completed <- struct{}{} }()
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			resultCh <- callbackResult{err: fmt.Errorf("Keycloak 拒绝授权: %s", oauthErr)}
			http.Error(w, "授权失败，可以关闭此窗口。", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code != "" {
			if err := exchangeCode(code, codeVerifier); err != nil {
				resultCh <- callbackResult{err: err}
				http.Error(w, "登录失败，请查看终端。", http.StatusBadGateway)
				return
			}
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

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		fmt.Printf("❌ 本地服务启动失败: %v\n", err)
		os.Exit(1)
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
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
	var callback callbackResult
	select {
	case callback = <-resultCh:
		<-completed
	case <-time.After(5 * time.Minute):
		callback.err = fmt.Errorf("登录等待超时，请重试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	if callback.err != nil {
		fmt.Printf("❌ 登录失败: %v\n", callback.err)
		os.Exit(1)
	}

	fmt.Printf("🎉 登录成功！临时 Token 已写入/替换: %s\n", tokenFile)
}

func exchangeCode(code, verifier string) error {
	unlock, err := acquireRefreshLock()
	if err != nil {
		return err
	}
	defer unlock()
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {verifier},
	}

	resp, err := client.PostForm(tokenEndpoint, form)
	if err != nil {
		return fmt.Errorf("请求 Token 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("换取 Token 失败（HTTP %d）", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("无法解析 Token 响应: %w", err)
	}

	accToken, _ := result["access_token"].(string)
	refToken, _ := result["refresh_token"].(string)
	expInRaw, _ := result["expires_in"].(float64)

	return saveTokens(accToken, refToken, int64(expInRaw))
}

// ==================== 主入口 ====================
func main() {
	force := flag.Bool("force", false, "强制弹窗重新登录，忽略本地缓存")
	flag.BoolVar(force, "f", false, "强制弹窗重新登录，忽略本地缓存")
	refreshOnly := flag.Bool("refresh-only", false, "仅静默刷新 Token；失败时不打开浏览器")
	flag.BoolVar(refreshOnly, "r", false, "仅静默刷新 Token；失败时不打开浏览器")
	flag.Parse()

	if *force && *refreshOnly {
		fmt.Println("❌ --force 与 --refresh-only 不能同时使用")
		os.Exit(2)
	}

	if *force {
		performBrowserLogin()
		return
	}

	if *refreshOnly {
		fmt.Println("🔄 正在静默刷新本地 Token...")
		unlock, err := acquireRefreshLock()
		if err != nil {
			fmt.Printf("❌ 无法获取刷新锁: %v\n", err)
			os.Exit(1)
		}

		ok, reason := tryRefreshToken()
		unlock()
		if !ok {
			fmt.Printf("❌ 静默刷新失败: %s\n", reason)
			fmt.Println("👉 请在终端运行 storehub-auth --force 重新登录。")
			os.Exit(1)
		}

		fmt.Printf("✅ Token 静默刷新成功: %s\n", tokenFile)
		return
	}

	fmt.Println("🔍 正在检查本地凭证状态，准备静默刷新...")
	unlock, err := acquireRefreshLock()
	if err != nil {
		fmt.Printf("⚠️ 无法获取刷新锁: %v\n", err)
		if expiresAt, ok := accessTokenHasAtLeast(0); ok {
			fmt.Printf("✅ 本地 Access Token 仍未过期，过期时间: %s\n👉 继续使用: %s\n", expiresAt.Local().Format(time.RFC3339), tokenFile)
			return
		}
		fmt.Println("ℹ️ 本地 Token 不可用，需要重新授信。")
		performBrowserLogin()
		return
	}
	ok, reason := tryRefreshToken()
	unlock()
	if ok {
		fmt.Printf("✨ 检测到有效会话，已在后台完成自动续期！\n👉 Token 已更新替换至: %s\n", tokenFile)
	} else {
		fmt.Printf("⚠️ 静默刷新失败: %s\n", reason)
		if expiresAt, ok := accessTokenHasAtLeast(0); ok {
			fmt.Printf("✅ 本地 Access Token 仍未过期，过期时间: %s\n👉 暂时继续使用: %s\n", expiresAt.Local().Format(time.RFC3339), tokenFile)
			return
		}
		fmt.Println("ℹ️ 本地 Token 已不可用，需要重新授信。")
		performBrowserLogin()
	}
}
