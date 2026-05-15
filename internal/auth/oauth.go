package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type DeviceFlow struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	httpClient   *http.Client
}

func NewDeviceFlow(clientID, clientSecret string, scopes []string) *DeviceFlow {
	return &DeviceFlow{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
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
}

func (d *DeviceFlow) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", d.ClientID)
	data.Set("scope", strings.Join(d.Scopes, " "))

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/device/code", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
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
	data.Set("client_id", d.ClientID)
	data.Set("client_secret", d.ClientSecret)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
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
		return nil, fmt.Errorf("token error: %s", result.Error)
	}

	return &oauth2.Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		TokenType:    result.TokenType,
		Expiry:       time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
	}, nil
}

func FormatTokenExpiry(expiresIn int) string {
	minutes := expiresIn / 60
	return fmt.Sprintf("~%d minutes", minutes)
}

func ParseExpiry(expiryStr string) (time.Time, error) {
	seconds, err := strconv.Atoi(expiryStr)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(time.Duration(seconds) * time.Second), nil
}
