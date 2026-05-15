package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
	"golang.org/x/oauth2"
)

const (
	ClientID     = "YOUR_CLIENT_ID.apps.googleusercontent.com"
	ClientSecret = "YOUR_CLIENT_SECRET"
)

var (
	Scopes = []string{
		"https://www.googleapis.com/auth/drive",
		"https://www.googleapis.com/auth/drive.file",
	}
)

type TokenStore struct {
	path       string
	identity   *age.X25519Identity
	recipient  *age.X25519Recipient
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
	return os.Remove(s.path)
}

type DeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func StartDeviceFlow(ctx context.Context) (*DeviceAuth, error) {
	url := "https://oauth2.googleapis.com/device/code"
	data := fmt.Sprintf("client_id=%s&scope=%s", ClientID, "https://www.googleapis.com/auth/drive")
	var result DeviceAuth
	// HTTP POST to url with data, parse JSON response
	// This is simplified - use proper HTTP client
	return &result, nil
}

func PollForToken(ctx context.Context, deviceCode string) (*oauth2.Token, error) {
	// Poll https://oauth2.googleapis.com/token until user authorizes
	// Return the token when granted
	return nil, nil
}
