package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coze-dev/coze-go"
	"github.com/gorilla/sessions"
)

const (
	CozeOAuthConfigPath = "coze_oauth_config.json"
	RedirectURI         = "http://127.0.0.1:8080/callback"
)

type Config struct {
	ClientType  string `json:"client_type"`
	ClientID    string `json:"client_id"`
	CozeDomain  string `json:"coze_www_base"`
	CozeAPIBase string `json:"coze_api_base"`
}

type TokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    string `json:"expires_in"`
}

var store = sessions.NewCookieStore([]byte("secret-key"))

func loadConfig() (*Config, error) {
	configFile, err := os.ReadFile(CozeOAuthConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("coze_oauth_config.json not found in current directory")
		}
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config Config
	if err := json.Unmarshal(configFile, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %v", err)
	}

	if config.ClientType != "pkce" {
		return nil, fmt.Errorf("invalid client type: %s, expect pkce", config.ClientType)
	}

	return &config, nil
}

func timestampToDateTime(timestamp int64) string {
	t := time.Unix(timestamp, 0)
	return t.Format("2006-01-02 15:04:05")
}

func readHTMLTemplate(filePath string) (string, error) {
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read template file: %v", err)
	}
	return string(content), nil
}

func renderTemplate(template string, data map[string]interface{}) string {
	result := template
	for key, value := range data {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, fmt.Sprintf("%v", value))
	}
	return result
}

func handleError(w http.ResponseWriter, config *Config, err error) {
	template, parseErr := readHTMLTemplate("websites/error.html")
	if parseErr != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"error":         err.Error(),
		"coze_www_base": config.CozeDomain,
	}

	w.WriteHeader(http.StatusInternalServerError)
	result := renderTemplate(template, data)
	w.Write([]byte(result))
}

func main() {
	log.SetFlags(0)

	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	oauth, err := coze.NewPKCEOAuthClient(config.ClientID, coze.WithAuthBaseURL(config.CozeAPIBase))
	if err != nil {
		log.Fatalf("Error creating OAuth client: %v", err)
	}

	fs := http.FileServer(http.Dir("assets"))
	http.Handle("/assets/", http.StripPrefix("/assets/", fs))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		template, err := readHTMLTemplate("websites/index.html")
		if err != nil {
			handleError(w, config, fmt.Errorf("failed to read template: %v", err))
			return
		}

		data := map[string]interface{}{
			"client_type": config.ClientType,
			"client_id":   config.ClientID,
		}

		result := renderTemplate(template, data)
		w.Write([]byte(result))
	})

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		resp, err := oauth.GetOAuthURL(ctx, &coze.GetPKCEOAuthURLReq{
			RedirectURI: RedirectURI,
			State:       "random",
		})
		if err != nil {
			handleError(w, config, fmt.Errorf("failed to get OAuth URL: %v", err))
			return
		}

		session, _ := store.Get(r, "pkce-session")
		session.Values["code_verifier"] = resp.CodeVerifier
		session.Save(r, w)

		http.Redirect(w, r, resp.AuthorizationURL, http.StatusFound)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			handleError(w, config, fmt.Errorf("authorization failed: no authorization code received"))
			return
		}

		session, _ := store.Get(r, "pkce-session")
		codeVerifier, ok := session.Values["code_verifier"].(string)
		if !ok {
			handleError(w, config, fmt.Errorf("authorization failed: no code verifier found"))
			return
		}

		ctx := context.Background()
		resp, err := oauth.GetAccessToken(ctx, &coze.GetPKCEAccessTokenReq{
			Code:         code,
			RedirectURI:  RedirectURI,
			CodeVerifier: codeVerifier,
		})
		if err != nil {
			handleError(w, config, fmt.Errorf("failed to get access token: %v", err))
			return
		}

		// Store token in session
		tokenSession, _ := store.Get(r, fmt.Sprintf("oauth_token_%s", config.ClientID))
		tokenSession.Values["token_type"] = "Bearer"
		tokenSession.Values["access_token"] = resp.AccessToken
		tokenSession.Values["refresh_token"] = resp.RefreshToken
		tokenSession.Values["expires_in"] = resp.ExpiresIn
		tokenSession.Save(r, w)

		expiresStr := fmt.Sprintf("%d (%s)", resp.ExpiresIn, timestampToDateTime(resp.ExpiresIn))

		template, err := readHTMLTemplate("websites/callback.html")
		if err != nil {
			handleError(w, config, fmt.Errorf("failed to read template: %v", err))
			return
		}

		data := map[string]interface{}{
			"token_type":    "Bearer",
			"access_token":  resp.AccessToken,
			"refresh_token": resp.RefreshToken,
			"expires_in":    expiresStr,
			"coze_www_base": config.CozeDomain,
		}

		result := renderTemplate(template, data)
		w.Write([]byte(result))
	})

	http.HandleFunc("/refresh_token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var requestData struct {
			RefreshToken string `json:"refresh_token"`
		}

		if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if requestData.RefreshToken == "" {
			http.Error(w, "No refresh token provided", http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		resp, err := oauth.RefreshToken(ctx, requestData.RefreshToken)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to refresh token: %v", err), http.StatusInternalServerError)
			return
		}

		// Update session
		session, _ := store.Get(r, fmt.Sprintf("oauth_token_%s", config.ClientID))
		session.Values["token_type"] = "Bearer"
		session.Values["access_token"] = resp.AccessToken
		session.Values["refresh_token"] = resp.RefreshToken
		session.Values["expires_in"] = resp.ExpiresIn
		session.Save(r, w)

		expiresStr := fmt.Sprintf("%d (%s)", resp.ExpiresIn, timestampToDateTime(resp.ExpiresIn))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token_type":    "Bearer",
			"access_token":  resp.AccessToken,
			"refresh_token": resp.RefreshToken,
			"expires_in":    expiresStr,
		})
	})

	log.Printf("Server starting on http://127.0.0.1:8080 (API Base: %s, Client Type: %s, Client ID: %s)\n",
		config.CozeAPIBase, config.ClientType, config.ClientID)
	if err := http.ListenAndServe("127.0.0.1:8080", nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
