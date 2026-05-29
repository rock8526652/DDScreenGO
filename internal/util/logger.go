package util

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ANSI 颜色转义字符 (高亮/明亮色系)
const (
	ColorReset  = "\033[0m"
	ColorCyan   = "\033[96m" // 高亮浅蓝/青色 (Sky Blue)
	ColorGray   = "\033[90m" // 灰色
	ColorGreen  = "\033[92m" // 高亮绿色
	ColorRed    = "\033[91m" // 高亮红色
	ColorYellow = "\033[93m" // 高亮黄色
	ColorPurple = "\033[95m" // 高亮紫色
)

var urlRegex = regexp.MustCompile(`https?://[^\s]+`)

// ColorURLs 匹配字符串中的 URL 并将其着色为浅蓝色 (Cyan)
func ColorURLs(msg string) string {
	return urlRegex.ReplaceAllStringFunc(msg, func(url string) string {
		return ColorCyan + url + ColorReset
	})
}

var GlobalLogLevel = "info"

func shouldLog(msgLevel string) bool {
	lvl := strings.ToLower(GlobalLogLevel)
	msg := strings.ToUpper(msgLevel)

	if lvl == "debug" || lvl == "dbg" {
		return true
	}
	if lvl == "info" || lvl == "inf" {
		return msg != "DBG" && msg != "DEBUG"
	}
	if lvl == "warn" || lvl == "wrn" {
		return msg == "WRN" || msg == "WARN" || msg == "ERR" || msg == "FAIL" || msg == "ERROR"
	}
	if lvl == "error" || lvl == "err" {
		return msg == "ERR" || msg == "FAIL" || msg == "ERROR"
	}
	return true
}

// Log 输出类似 Serilog 风格的带颜色日志线
func Log(level, context, format string, args ...any) {
	if !shouldLog(level) {
		return
	}

	ts := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	msg = ColorURLs(msg)

	levelColored := level
	switch strings.ToUpper(level) {
	case "INF", "INFO":
		levelColored = ColorCyan + "INF" + ColorReset
	case "WRN", "WARN":
		levelColored = ColorYellow + "WRN" + ColorReset
	case "ERR", "FAIL":
		levelColored = ColorRed + "ERR" + ColorReset
	}

	fmt.Printf("%s[%s %s]%s %s%s%s - %s\n",
		ColorGray, ts, levelColored, ColorGray,
		ColorGray, context, ColorReset,
		msg,
	)
}

// LogRequest 输出结构化、带颜色的 HTTP 请求记录日志
func LogRequest(method, uri string, status int, elapsedMs int64) {
	msgLevel := "INF"
	if status >= 400 {
		msgLevel = "ERR"
	} else if status >= 300 {
		msgLevel = "WRN"
	}
	if !shouldLog(msgLevel) {
		return
	}

	ts := time.Now().Format("15:04:05")
	levelColored := ColorCyan + msgLevel + ColorReset
	if msgLevel == "ERR" {
		levelColored = ColorRed + "ERR" + ColorReset
	} else if msgLevel == "WRN" {
		levelColored = ColorYellow + "WRN" + ColorReset
	}

	// 请求方法着色 (紫色)
	methodColored := ColorPurple + method + ColorReset

	// 访问路径/连接着色 (浅蓝色)
	uriColored := ColorCyan + uri + ColorReset

	// 状态码着色 (2xx绿，3xx黄，其他红)
	statusColored := fmt.Sprintf("%d", status)
	if status >= 200 && status < 300 {
		statusColored = ColorGreen + statusColored + ColorReset
	} else if status >= 300 && status < 400 {
		statusColored = ColorYellow + statusColored + ColorReset
	} else {
		statusColored = ColorRed + statusColored + ColorReset
	}

	// 响应耗时着色 (浅蓝色)
	elapsedColored := fmt.Sprintf("%s%dms%s", ColorCyan, elapsedMs, ColorReset)

	fmt.Printf("%s[%s %s]%s %sHTTPAPI%s - %s %s - %s - %s\n",
		ColorGray, ts, levelColored, ColorGray,
		ColorGray, ColorReset,
		methodColored, uriColored, statusColored, elapsedColored,
	)
}
