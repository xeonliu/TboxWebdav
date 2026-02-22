package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AuthMode defines how WebDAV clients authenticate.
type AuthMode int

const (
	AuthModeNone      AuthMode = iota
	AuthModeJaCookie           // authenticate via JAAuthCookie
	AuthModeUserToken          // authenticate via UserToken
	AuthModeCustom             // authenticate via custom user list
	AuthModeMixed              // try UserToken, JaCookie, then Custom
)

func (a AuthMode) String() string {
	switch a {
	case AuthModeNone:
		return "None"
	case AuthModeJaCookie:
		return "JaCookie"
	case AuthModeUserToken:
		return "UserToken"
	case AuthModeCustom:
		return "Custom"
	case AuthModeMixed:
		return "Mixed"
	default:
		return "Unknown"
	}
}

// ParseAuthMode parses an auth mode string (case-insensitive).
func ParseAuthMode(s string) (AuthMode, error) {
	switch strings.ToLower(s) {
	case "none":
		return AuthModeNone, nil
	case "jacookie":
		return AuthModeJaCookie, nil
	case "usertoken":
		return AuthModeUserToken, nil
	case "custom":
		return AuthModeCustom, nil
	case "mixed":
		return AuthModeMixed, nil
	default:
		return AuthModeMixed, fmt.Errorf("unknown auth mode: %s", s)
	}
}

// AccessMode defines what operations are permitted.
type AccessMode int

const (
	AccessModeFull     AccessMode = iota
	AccessModeReadOnly            // no writes at all
	AccessModeNoDelete            // writes allowed but no deletes
)

func (a AccessMode) String() string {
	switch a {
	case AccessModeFull:
		return "Full"
	case AccessModeReadOnly:
		return "ReadOnly"
	case AccessModeNoDelete:
		return "NoDelete"
	default:
		return "Unknown"
	}
}

// ParseAccessMode parses an access mode string (case-insensitive).
func ParseAccessMode(s string) (AccessMode, error) {
	switch strings.ToLower(s) {
	case "full":
		return AccessModeFull, nil
	case "readonly":
		return AccessModeReadOnly, nil
	case "nodelete":
		return AccessModeNoDelete, nil
	default:
		return AccessModeFull, fmt.Errorf("unknown access mode: %s", s)
	}
}

// CustomUser holds per-user credentials for Custom/Mixed auth mode.
type CustomUser struct {
	UserName   string
	Password   string
	Cookie     string
	UserToken  string
	AccessMode AccessMode
}

// Config holds the full application configuration.
type Config struct {
	Host       string
	Port       int
	CacheSize  int
	AuthMode   AuthMode
	AccessMode AccessMode
	Cookie     string
	UserToken  string
	Users      []CustomUser
}

// yamlRoot mirrors the YAML file structure.
type yamlRoot struct {
	Host       string     `yaml:"Host"`
	Port       int        `yaml:"Port"`
	CacheSize  int        `yaml:"CacheSize"`
	AuthMode   string     `yaml:"AuthMode"`
	AccessMode string     `yaml:"AccessMode"`
	Cookie     string     `yaml:"Cookie"`
	UserToken  string     `yaml:"UserToken"`
	Users      []yamlUser `yaml:"Users"`
}

type yamlUser struct {
	UserName   string `yaml:"UserName"`
	Password   string `yaml:"Password"`
	Cookie     string `yaml:"Cookie"`
	UserToken  string `yaml:"UserToken"`
	AccessMode string `yaml:"AccessMode"`
}

// LoadFromFile loads a Config from a YAML file, applying defaults where fields are absent.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var yc yamlRoot
	if err := yaml.Unmarshal(data, &yc); err != nil {
		return nil, err
	}

	cfg := &Config{
		Host:      "localhost",
		Port:      65472,
		CacheSize: 20 * 1024 * 1024,
		AuthMode:  AuthModeMixed,
	}

	if yc.Host != "" {
		cfg.Host = yc.Host
	}
	if yc.Port != 0 {
		cfg.Port = yc.Port
	}
	if yc.CacheSize != 0 {
		cfg.CacheSize = yc.CacheSize
	}
	if yc.AuthMode != "" {
		am, err := ParseAuthMode(yc.AuthMode)
		if err != nil {
			return nil, err
		}
		cfg.AuthMode = am
	}
	if yc.AccessMode != "" {
		ac, err := ParseAccessMode(yc.AccessMode)
		if err != nil {
			return nil, err
		}
		cfg.AccessMode = ac
	}
	cfg.Cookie = yc.Cookie
	cfg.UserToken = yc.UserToken

	for _, u := range yc.Users {
		am := AccessModeFull
		if u.AccessMode != "" {
			parsed, err := ParseAccessMode(u.AccessMode)
			if err != nil {
				// Default to Full and continue; unknown values are silently ignored.
				_ = err
			} else {
				am = parsed
			}
		}
		cfg.Users = append(cfg.Users, CustomUser{
			UserName:   u.UserName,
			Password:   u.Password,
			Cookie:     u.Cookie,
			UserToken:  u.UserToken,
			AccessMode: am,
		})
	}

	return cfg, nil
}
