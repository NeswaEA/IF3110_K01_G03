package platform

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv                  string
	HTTPAddr                string
	AppOrigin               string
	MetadataDatabaseURL     string `json:"-"`
	SessionIdleTimeout      time.Duration
	SessionAbsoluteTimeout  time.Duration
	SessionRotationInterval time.Duration
	SessionGracePeriod      time.Duration
	CookieSecure            bool
}

func LoadConfig() (Config, error) {
	cfg := Config{
		AppEnv:              os.Getenv("APP_ENV"),
		HTTPAddr:            os.Getenv("HTTP_ADDR"),
		AppOrigin:           os.Getenv("APP_ORIGIN"),
		MetadataDatabaseURL: os.Getenv("METADATA_DATABASE_URL"),
	}
	if cfg.AppEnv == "" {
		cfg.AppEnv = "development"
	}
	switch cfg.AppEnv {
	case "development", "test", "production":
	default:
		return Config{}, errors.New("APP_ENV must be development, test, or production")
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, errors.New("HTTP_ADDR must use host:port format")
	}
	if !validPort(port) {
		return Config{}, errors.New("HTTP_ADDR port must be between 1 and 65535")
	}
	origin, err := url.Parse(cfg.AppOrigin)
	if err != nil || origin.Hostname() == "" || (origin.Scheme != "http" && origin.Scheme != "https") ||
		origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return Config{}, errors.New("APP_ORIGIN must be an HTTP(S) origin without credentials, query, fragment, or non-root path")
	}
	if port := origin.Port(); port != "" && !validPort(port) {
		return Config{}, errors.New("APP_ORIGIN port must be between 1 and 65535")
	}
	cfg.AppOrigin = origin.Scheme + "://" + origin.Host
	if strings.TrimSpace(cfg.MetadataDatabaseURL) == "" {
		return Config{}, errors.New("METADATA_DATABASE_URL is required")
	}
	for _, setting := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{"SESSION_IDLE_TIMEOUT", &cfg.SessionIdleTimeout, 30 * time.Minute},
		{"SESSION_ABSOLUTE_TIMEOUT", &cfg.SessionAbsoluteTimeout, 8 * time.Hour},
		{"SESSION_ROTATION_INTERVAL", &cfg.SessionRotationInterval, 15 * time.Minute},
		{"SESSION_GRACE_PERIOD", &cfg.SessionGracePeriod, 30 * time.Second},
	} {
		*setting.value = setting.fallback
		if raw := os.Getenv(setting.name); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value < time.Microsecond || value%time.Microsecond != 0 {
				return Config{}, errors.New(setting.name + " must be a positive duration in whole microseconds")
			}
			*setting.value = value
		}
	}
	if raw := os.Getenv("COOKIE_SECURE"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, errors.New("COOKIE_SECURE must be a boolean")
		}
		cfg.CookieSecure = value
	}
	cfg.CookieSecure = cfg.CookieSecure || cfg.AppEnv == "production" || origin.Scheme == "https"
	return cfg, nil
}

func validPort(port string) bool {
	for _, char := range port {
		if char < '0' || char > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}
