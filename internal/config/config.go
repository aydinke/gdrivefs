package config

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mounts    map[string]MountConfig `yaml:"mounts"`
	ClientID  string                 `yaml:"client_id"`
	ClientSecret string              `yaml:"client_secret"`
}

type MountConfig struct {
	Path        string `yaml:"path"`
	AutoMount   bool   `yaml:"auto_mount"`
	ReadOnly    bool   `yaml:"read_only"`
}

var (
	cfg     *Config
	cfgOnce sync.Once
	cfgPath string
)

func Init() error {
	var err error
	cfgOnce.Do(func() {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".config", "gdrivefs", "config.yaml")
		cfg, err = Load()
	})
	return err
}

func Load() (*Config, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Mounts: make(map[string]MountConfig)}, nil
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Mounts == nil {
		c.Mounts = make(map[string]MountConfig)
	}
	return &c, nil
}

func Save(c *Config) error {
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0600)
}

func Get() *Config {
	return cfg
}

func SetPath(path string) {
	cfgPath = path
}

func GetDataPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "gdrivefs")
}

func GetTokenPath() string {
	return filepath.Join(GetDataPath(), "token.enc")
}

func GetUploadsPath() string {
	return filepath.Join(GetDataPath(), "uploads")
}
