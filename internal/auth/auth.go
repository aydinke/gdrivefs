package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"golang.org/x/oauth2"
)

const (
	DefaultClientID     = ""
	DefaultClientSecret = ""
)

var (
	Scopes = []string{
		"https://www.googleapis.com/auth/drive",
	}
)

type Credentials struct {
	ClientID     string
	ClientSecret string
}

func DefaultCredentials() *Credentials {
	return &Credentials{
		ClientID:     DefaultClientID,
		ClientSecret: DefaultClientSecret,
	}
}

func CredentialsFromEnv() *Credentials {
	return &Credentials{
		ClientID:     os.Getenv("GDRIVEFS_CLIENT_ID"),
		ClientSecret: os.Getenv("GDRIVEFS_CLIENT_SECRET"),
	}
}

func (c *Credentials) IsValid() bool {
	return c.ClientID != "" &&
		c.ClientSecret != "" &&
		strings.Contains(c.ClientID, ".apps.googleusercontent.com")
}

type TokenStore struct {
	path      string
	identity  *age.X25519Identity
	recipient *age.X25519Recipient
}

func NewTokenStore(path string) (*TokenStore, error) {
	identity, recipient, err := loadOrGenerateIdentity(path)
	if err != nil {
		return nil, err
	}
	return &TokenStore{
		path:      path,
		identity:  identity,
		recipient: recipient,
	}, nil
}

func loadOrGenerateIdentity(path string) (*age.X25519Identity, *age.X25519Recipient, error) {
	keyPath := path + ".key"
	data, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return generateAndSaveIdentity(keyPath)
		}
		return nil, nil, err
	}
	identity, err := age.ParseX25519Identity(string(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse identity: %w", err)
	}
	return identity, identity.Recipient(), nil
}

func generateAndSaveIdentity(keyPath string) (*age.X25519Identity, *age.X25519Recipient, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate identity: %w", err)
	}
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(keyPath, []byte(identity.String()), 0600); err != nil {
		return nil, nil, err
	}
	return identity, identity.Recipient(), nil
}

func (s *TokenStore) Save(token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	outFile, err := os.Create(s.path)
	if err != nil {
		return err
	}
	defer outFile.Close()
	w, err := age.Encrypt(outFile, s.recipient)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = w.Write(data)
	return err
}

func (s *TokenStore) Load() (*oauth2.Token, error) {
	inFile, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer inFile.Close()
	r, err := age.Decrypt(inFile, s.identity)
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.NewDecoder(r).Decode(&token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *TokenStore) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

func (s *TokenStore) Delete() error {
	keyPath := s.path + ".key"
	os.Remove(keyPath)
	return os.Remove(s.path)
}

type OAuthFlow struct {
	Credentials *Credentials
	Port        int
	httpClient  *http.Client
}

func NewOAuthFlow(creds *Credentials, port int) *OAuthFlow {
	if port == 0 {
		port = 8085
	}
	return &OAuthFlow{
		Credentials: creds,
		Port:        port,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

type AuthResult struct {
	Token *oauth2.Token
	Error error
}

func (f *OAuthFlow) StartLocalServer(ctx context.Context) (*oauth2.Token, error) {
	resultChan := make(chan AuthResult, 1)

	handler := http.NewServeMux()
	handler.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errorDesc := r.URL.Query().Get("error_description")
			if errorDesc == "" {
				errorDesc = r.URL.Query().Get("error")
			}
			http.Error(w, "Error: "+errorDesc, http.StatusBadRequest)
			resultChan <- AuthResult{Error: fmt.Errorf("oauth error: %s", errorDesc)}
			return
		}

		token, err := f.exchangeCode(ctx, code)
		if err != nil {
			http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
			resultChan <- AuthResult{Error: err}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
			<html>
			<head><title>Success</title></head>
			<body style="font-family: sans-serif; text-align: center; padding: 50px;">
				<h1 style="color: green;">✓ Success!</h1>
				<p>You can close this window and return to the terminal.</p>
			</body>
			</html>
		`))
		resultChan <- AuthResult{Token: token}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", f.Port),
		Handler: handler,
	}

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			resultChan <- AuthResult{Error: err}
		}
	}()

	authURL := f.getAuthURL()
	fmt.Printf("\nOpening browser to:\n%s\n\n", authURL)
	fmt.Println("If the browser doesn't open automatically, visit the URL above.")

	err := openBrowser(authURL)
	if err != nil {
		fmt.Printf("Could not open browser: %v\n", err)
		fmt.Println("Please visit the URL manually.")
	}

	select {
	case result := <-resultChan:
		server.Shutdown(ctx)
		return result.Token, result.Error
	case <-ctx.Done():
		server.Shutdown(ctx)
		return nil, ctx.Err()
	}
}

func (f *OAuthFlow) getAuthURL() string {
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", f.Port)
	params := url.Values{}
	params.Set("client_id", f.Credentials.ClientID)
	params.Set("redirect_uri", redirectURL)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(Scopes, " "))
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")

	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
}

func (f *OAuthFlow) exchangeCode(ctx context.Context, code string) (*oauth2.Token, error) {
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", f.Port)
	data := url.Values{}
	data.Set("client_id", f.Credentials.ClientID)
	data.Set("client_secret", f.Credentials.ClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURL)

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://oauth2.googleapis.com/token",
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("%s: %s", result.Error, result.ErrorDesc)
	}

	return &oauth2.Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		TokenType:    result.TokenType,
		Expiry:       time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
	}, nil
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch {
	case commandExists("xdg-open"):
		cmd = "xdg-open"
		args = []string{url}
	case commandExists("google-chrome"):
		cmd = "google-chrome"
		args = []string{url}
	case commandExists("firefox"):
		cmd = "firefox"
		args = []string{url}
	default:
		return fmt.Errorf("no browser found")
	}

	return exec.Command(cmd, args...).Start()
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
