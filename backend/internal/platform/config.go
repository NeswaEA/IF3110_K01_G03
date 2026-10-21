package platform

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv              string
	HTTPAddr            string
	AppOrigin           string
	MetadataDatabaseURL string `json:"-"`
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
