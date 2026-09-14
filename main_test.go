package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupCredentials(t *testing.T) {
	t.Helper()
	oldDir, oldToken, oldCreds, oldLock, oldEndpoint := configDir, tokenFile, credentialsFile, refreshLockFile, tokenEndpoint
	t.Cleanup(func() {
		configDir, tokenFile, credentialsFile, refreshLockFile, tokenEndpoint = oldDir, oldToken, oldCreds, oldLock, oldEndpoint
	})
	configDir = t.TempDir()
	tokenFile = filepath.Join(configDir, "token.txt")
	credentialsFile = filepath.Join(configDir, "credentials.json")
	refreshLockFile = filepath.Join(configDir, "refresh.lock")
	if err := saveTokens("old-access", "old-refresh", 7200); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshRotationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		wantOK      bool
		wantRefresh string
	}{
		{"rotation", `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200}`, 200, true, "new-refresh"},
		{"retain", `{"access_token":"new-access","expires_in":7200}`, 200, true, "old-refresh"},
		{"expired", `{"error":"invalid_grant"}`, 400, false, "old-refresh"},
		{"invalid lifetime", `{"access_token":"new-access","refresh_token":"new-refresh"}`, 200, false, "old-refresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupCredentials(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != ClientID || r.Form.Get("grant_type") != "refresh_token" {
					t.Error("incorrect refresh request")
				}
				if r.Form.Get("client_secret") != "" {
					t.Error("public client sent secret")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			tokenEndpoint = srv.URL
			ok, reason := tryRefreshToken()
			if ok != tc.wantOK {
				t.Fatalf("ok=%v reason=%s", ok, reason)
			}
			b, _ := os.ReadFile(credentialsFile)
			var creds Credentials
			json.Unmarshal(b, &creds)
			if creds.RefreshToken != tc.wantRefresh {
				t.Fatal("wrong saved refresh token")
			}
			b, _ = os.ReadFile(tokenFile)
			want := "old-access"
			if tc.wantOK {
				want = "new-access"
			}
			if string(b) != want {
				t.Fatal("access token corrupted")
			}
			for _, p := range []string{tokenFile, credentialsFile} {
				info, _ := os.Stat(p)
				if info.Mode().Perm() != 0600 {
					t.Fatal("incorrect credentials permissions")
				}
			}
		})
	}
}

func TestCodeExchange(t *testing.T) {
	setupCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code_verifier") != "verifier" || r.Form.Get("code") != "code" || r.Form.Get("redirect_uri") != RedirectURI {
			t.Error("incorrect PKCE request")
		}
		w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200}`))
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL
	if err := exchangeCode("code", "verifier"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(refreshLockFile); !os.IsNotExist(err) {
		t.Fatal("refresh lock leaked")
	}
}
