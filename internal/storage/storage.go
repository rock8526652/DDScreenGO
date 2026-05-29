package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct {
	BaseDir       string
	ScreenshotDir string
	LogDir        string
}

type Cookie struct {
	Name     string  `json:"Name"`
	Value    string  `json:"Value"`
	Domain   string  `json:"Domain,omitempty"`
	Path     string  `json:"Path,omitempty"`
	Secure   bool    `json:"Secure,omitempty"`
	HTTPOnly bool    `json:"HttpOnly,omitempty"`
	Expires  float64 `json:"Expires,omitempty"`
}

func New(baseDir string) *Store {
	return &Store{
		BaseDir:       baseDir,
		ScreenshotDir: filepath.Join(baseDir, "ScreenShotImg"),
		LogDir:        filepath.Join(baseDir, "logs"),
	}
}

func EnsureRuntimeDirs(baseDir string) error {
	for _, dir := range []string{
		filepath.Join(baseDir, "ScreenShotImg"),
		filepath.Join(baseDir, "logs"),
		filepath.Join(baseDir, "ChromeProfile"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SavePNG(prefix string, data []byte) (string, string, error) {
	if err := os.MkdirAll(s.ScreenshotDir, 0o755); err != nil {
		return "", "", err
	}
	name := fmt.Sprintf("%s_%s_%d.png", sanitizePrefix(prefix), time.Now().Format("20060102150405"), time.Now().UnixNano())
	path := filepath.Join(s.ScreenshotDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", err
	}
	return path, "/ScreenShotImg/" + name, nil
}

func (s *Store) CookiePath(platform string) string {
	switch strings.ToLower(platform) {
	case "bili", "bilibili":
		return filepath.Join(s.BaseDir, "Bili_Cookies.json")
	case "weibo":
		return filepath.Join(s.BaseDir, "Weibo_Cookies.json")
	case "douyin":
		return filepath.Join(s.BaseDir, "Douyin_Cookies.json")
	default:
		return filepath.Join(s.BaseDir, platform+"_Cookies.json")
	}
}

func (s *Store) ReadCookies(platform string) ([]Cookie, error) {
	data, err := os.ReadFile(s.CookiePath(platform))
	if err != nil {
		return nil, err
	}
	var cookies []Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, err
	}
	return cookies, nil
}

func (s *Store) WriteCookies(platform string, cookies []Cookie) error {
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.CookiePath(platform), data, 0o600)
}

func (s *Store) CookieHeader(platform string) string {
	cookies, err := s.ReadCookies(platform)
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name != "" && c.Value != "" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func (s *Store) StartCleaner(ctx context.Context, interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.CleanScreenshots(maxAge)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) CleanScreenshots(maxAge time.Duration) {
	entries, err := os.ReadDir(s.ScreenshotDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".png") {
			continue
		}
		path := filepath.Join(s.ScreenshotDir, entry.Name())
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func sanitizePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(prefix) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "image"
	}
	return b.String()
}
