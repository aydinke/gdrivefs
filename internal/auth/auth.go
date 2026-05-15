package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

type DeviceFlow struct {
	Credentials *Credentials
	httpClient   *http.Client
}

func NewDeviceFlow(creds *Credentials) *DeviceFlow {
	return &DeviceFlow{
		Credentials: creds,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (d *DeviceFlow) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", d.Credentials.ClientID)
	data.Set("scope", strings.Join(Scopes, " "))

	req, err := http.NewRequestWithContext(ctx, "POST", 
		"https://oauth2.googleapis.com/device/code", 
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}

	var result DeviceCodeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	return &result, nil
}

func (d *DeviceFlow) PollForToken(ctx context.Context, deviceCode string, interval int) (*oauth2.Token, error) {
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			token, err := d.requestToken(ctx, deviceCode)
			if err != nil {
				return nil, err
			}
			if token != nil {
				return token, nil
			}
		}
	}
}

func (d *DeviceFlow) requestToken(ctx context.Context, deviceCode string) (*oauth2.Token, error) {
	data := url.Values{}
	data.Set("client_id", d.Credentials.ClientID)
	data.Set("client_secret", d.Credentials.ClientSecret)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", 
		"https://oauth2.googleapis.com/token", 
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}

	var result TokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if result.Error != "" {
		if result.Error == "authorization_pending" {
			return nil, nil
		}
		if result.Error == "slow_down" {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %s", result.Error, result.ErrorDesc)
	}

	return &oauth2.Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		TokenType:    result.TokenType,
		Expiry:       time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
	}, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return readAll(resp.Body)
}

func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
