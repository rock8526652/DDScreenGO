package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

type Config struct {
	BaseDir    string `json:"-"`
	ListenAddr string `json:"ListenAddr"`
	ChromePath string `json:"ChromePath"`
	Headless   bool   `json:"Headless"`
	Debug      bool   `json:"Debug"`
	LogLevel   string `json:"LogLevel"`
}

func Load() (Config, error) {
	base, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		BaseDir:    base,
		ListenAddr: "127.0.0.1:7000",
		Headless:   true,
		LogLevel:   "info",
	}

	// 检查配置文件是否存在，若不存在则自动生成默认的 appsettings.json
	hasConfig := false
	for _, name := range []string{"appsettings.json", "config.json"} {
		path := filepath.Join(base, name)
		if _, err := os.Stat(path); err == nil {
			hasConfig = true
			break
		}
	}
	if !hasConfig {
		jsonPath := filepath.Join(base, "appsettings.json")
		jsonContent := `{
  "//_ListenAddr": "服务监听的 IP 地址和端口号。默认是 127.0.0.1:7000，也可以改为 0.0.0.0:7000 让局域网内其他设备访问",
  "ListenAddr": "127.0.0.1:7000",

  "//_ChromePath": "谷歌浏览器 Chrome 的可执行文件安装路径。如果为空，程序会自动在系统的常见安装路径和程序当前目录下寻找",
  "ChromePath": "",

  "//_Headless": "是否使用无头模式运行浏览器（即隐藏 Chrome 的实际运行窗口）。设置为 true 时浏览器在后台默默运行，设置为 false 时会弹出浏览器实体窗口（调试用）",
  "Headless": true,

  "//_Debug": "是否启用调试模式。设置为 true 时，会屏蔽所有带有 [Disableable] 拦截属性的敏感 API 接口，直接返回 404；生产环境下通常设为 false 以全面放开接口访问",
  "Debug": false,

  "//_LogLevel": "日志输出级别: debug, info, warn, error。设为 error 时，只输出错误信息及 400/500 的异常请求。",
  "LogLevel": "info"
}
`
		_ = os.WriteFile(jsonPath, []byte(jsonContent), 0644)
	}

	for _, name := range []string{"appsettings.json", "config.json"} {
		path := filepath.Join(base, name)
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &cfg)
			break
		}
	}

	if v := os.Getenv("DD_SCREEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("ChromePath"); v != "" {
		cfg.ChromePath = v
	}
	if v := os.Getenv("DD_CHROME_PATH"); v != "" {
		cfg.ChromePath = v
	}
	if v := os.Getenv("Headless"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Headless = b
		}
	}
	if v := os.Getenv("Debug"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Debug = b
		}
	}
	if v := os.Getenv("LogLevel"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	if cfg.ChromePath == "" {
		cfg.ChromePath = findChrome(base)
	}
	if cfg.ChromePath == "" {
		return cfg, errors.New("ChromePath 未配置，且未在程序目录/常见安装位置找到 Chrome")
	}
	return cfg, nil
}

func findChrome(base string) string {
	candidates := []string{}
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates,
			filepath.Join(base, "Chrome", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		)
	case "darwin":
		candidates = append(candidates,
			filepath.Join(base, "Chrome", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	default:
		candidates = append(candidates,
			filepath.Join(base, "Chrome", "chrome"),
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
		)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}
