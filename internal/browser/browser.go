package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"dd_screen_go/internal/util"

	"github.com/gorilla/websocket"
)

type Browser struct {
	chromePath string
	headless   bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	port    int
	tempDir string
}

type Session struct {
	browser  *Browser
	targetID string
	port     int
	ws       *websocket.Conn
	tempDir  string

	nextID  atomic.Int64
	pending map[int64]chan cdpMessage
	mu      sync.Mutex
	events  chan cdpMessage
	closed  chan struct{}
}

type targetInfo struct {
	ID                   string `json:"id"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_, _ = b.cmd.Process.Wait()
		b.cmd = nil
	}
	if b.tempDir != "" {
		_ = os.RemoveAll(b.tempDir)
		b.tempDir = ""
	}
}

func (b *Browser) startProcess(ctx context.Context) error {
	if b.cmd != nil && b.cmd.Process != nil {
		if b.cmd.ProcessState == nil || !b.cmd.ProcessState.Exited() {
			return nil
		}
		_ = b.cmd.Process.Kill()
		_, _ = b.cmd.Process.Wait()
		if b.tempDir != "" {
			_ = os.RemoveAll(b.tempDir)
		}
		b.cmd = nil
	}

	port, err := freePort()
	if err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "dd-screen-go-chrome-*")
	if err != nil {
		return err
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir=" + tempDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--disable-web-security",
		"--hide-scrollbars",
		"--mute-audio",
		"--no-sandbox",
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"about:blank",
	}
	if b.headless {
		args = append([]string{"--headless=new"}, args...)
	}

	cmd := exec.Command(b.chromePath, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}

	go func() {
		_ = cmd.Wait()
	}()

	_, err = waitPageWebSocket(ctx, port)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(tempDir)
		return err
	}

	b.cmd = cmd
	b.port = port
	b.tempDir = tempDir
	return nil
}

func (b *Browser) createNewTab(ctx context.Context) (targetInfo, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/json/new", b.port), nil)
	if err != nil {
		return targetInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return targetInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return targetInfo{}, fmt.Errorf("创建新标签页失败: HTTP %d", resp.StatusCode)
	}
	var info targetInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return targetInfo{}, err
	}
	return info, nil
}

func (b *Browser) closeTab(ctx context.Context, targetID string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/close/%s", b.port, targetID), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
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

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type cdpMessage struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func New(chromePath string, headless bool) *Browser {
	return &Browser{chromePath: chromePath, headless: headless}
}

func (b *Browser) NewSession(ctx context.Context) (*Session, error) {
	if b.chromePath == "" {
		return nil, errors.New("ChromePath 未配置")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.startProcess(ctx); err != nil {
		return nil, err
	}

	util.Log("DBG", "Browser", "正在创建 Chrome 新标签页...")
	targetInfo, err := b.createNewTab(ctx)
	if err != nil {
		return nil, err
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, targetInfo.WebSocketDebuggerURL, nil)
	if err != nil {
		_ = b.closeTab(ctx, targetInfo.ID)
		return nil, err
	}

	s := &Session{
		browser:  b,
		targetID: targetInfo.ID,
		port:     b.port,
		ws:       conn,
		tempDir:  b.tempDir,
		pending:  map[int64]chan cdpMessage{},
		events:   make(chan cdpMessage, 256),
		closed:   make(chan struct{}),
	}
	go s.readLoop()

	_, _ = s.Do(ctx, "Page.enable", nil)
	_, _ = s.Do(ctx, "Runtime.enable", nil)
	_, _ = s.Do(ctx, "DOM.enable", nil)
	_, _ = s.Do(ctx, "Network.enable", nil)
	_, _ = s.Do(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": `
			Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
			window.chrome = { runtime: {} };
			Object.defineProperty(navigator, 'languages', {get: () => ['zh-CN', 'zh']});
		`,
	})
	
	util.Log("DBG", "Browser", "Chrome 新会话已建立 (TargetID: %s)", targetInfo.ID)
	return s, nil
}

func (s *Session) Do(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan cdpMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := s.ws.WriteJSON(req); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errors.New("browser session closed")
	}
}

func (s *Session) SetViewport(ctx context.Context, width, height int, scale float64, mobile bool) error {
	_, err := s.Do(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": scale,
		"mobile":            mobile,
	})
	return err
}

func (s *Session) Navigate(ctx context.Context, rawURL string, wait time.Duration) error {
	util.Log("DBG", "Browser", "页面开始导航至: %s", rawURL)
	_, err := s.Do(ctx, "Page.navigate", map[string]any{"url": rawURL})
	if err != nil {
		return err
	}
	return s.WaitLoad(ctx, wait)
}

func (s *Session) SetContent(ctx context.Context, html string) error {
	path := filepath.Join(s.tempDir, fmt.Sprintf("page-%s.html", s.targetID))
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return err
	}
	return s.Navigate(ctx, fileURL(path), 15*time.Second)
}

func (s *Session) WaitLoad(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-s.events:
			if ev.Method == "Page.loadEventFired" || ev.Method == "Page.domContentEventFired" {
				return nil
			}
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return errors.New("browser session closed")
		}
	}
}

func (s *Session) Eval(ctx context.Context, expression string) (json.RawMessage, error) {
	res, err := s.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Result.Value, nil
}

func (s *Session) WaitExpr(ctx context.Context, expression string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastRaw json.RawMessage
	var lastErr error
	for {
		raw, err := s.Eval(ctx, expression)
		lastRaw = raw
		lastErr = err
		if err == nil {
			var ok bool
			if json.Unmarshal(raw, &ok) == nil && ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			titleRaw, _ := s.Eval(ctx, "document.title")
			urlRaw, _ := s.Eval(ctx, "window.location.href")
			return fmt.Errorf("等待页面条件超时 (页面标题: %s, 页面链接: %s, 评估错误: %v, 最后结果: %s)", 
				string(titleRaw), string(urlRaw), lastErr, string(lastRaw))
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Session) ElementRect(ctx context.Context, selector string) (Rect, error) {
	expr := fmt.Sprintf(`(() => {
const el = document.querySelector(%q);
if (!el) return null;
const r = el.getBoundingClientRect();
return {x:r.x + window.scrollX, y:r.y + window.scrollY, width:r.width, height:r.height};
})()`, selector)
	raw, err := s.Eval(ctx, expr)
	if err != nil {
		return Rect{}, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return Rect{}, fmt.Errorf("找不到元素: %s", selector)
	}
	var rect Rect
	if err := json.Unmarshal(raw, &rect); err != nil {
		return Rect{}, err
	}
	if rect.Width <= 0 || rect.Height <= 0 {
		return Rect{}, fmt.Errorf("元素尺寸异常: %s", selector)
	}
	return rect, nil
}

func (s *Session) Screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error) {
	params := map[string]any{
		"format":                "png",
		"captureBeyondViewport": true,
		"fromSurface":           true,
	}
	if selector != "" {
		rect, err := s.ElementRect(ctx, selector)
		if err != nil {
			return nil, err
		}
		params["clip"] = map[string]any{
			"x":      maxFloat(0, rect.X),
			"y":      maxFloat(0, rect.Y),
			"width":  maxFloat(1, rect.Width),
			"height": maxFloat(1, rect.Height),
			"scale":  1,
		}
	} else if fullPage {
		metrics, err := s.Do(ctx, "Page.getLayoutMetrics", nil)
		if err == nil {
			var m struct {
				ContentSize Rect `json:"contentSize"`
			}
			if json.Unmarshal(metrics, &m) == nil && m.ContentSize.Width > 0 && m.ContentSize.Height > 0 {
				params["clip"] = map[string]any{
					"x":      0,
					"y":      0,
					"width":  m.ContentSize.Width,
					"height": m.ContentSize.Height,
					"scale":  1,
				}
			}
		}
	}

	res, err := s.Do(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data)
}

func (s *Session) Click(ctx context.Context, x, y int) error {
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		_, err := s.Do(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":       typ,
			"x":          x,
			"y":          y,
			"button":     "left",
			"buttons":    1,
			"clickCount": 1,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) Cookies(ctx context.Context) ([]Cookie, error) {
	res, err := s.Do(ctx, "Network.getAllCookies", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Secure   bool    `json:"secure"`
			HTTPOnly bool    `json:"httpOnly"`
			Expires  float64 `json:"expires"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	cookies := make([]Cookie, 0, len(out.Cookies))
	for _, c := range out.Cookies {
		cookies = append(cookies, Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: c.Expires,
		})
	}
	return cookies, nil
}

func (s *Session) SetCookie(ctx context.Context, name, value, domain, rawURL string) error {
	params := map[string]any{
		"name":  name,
		"value": value,
		"path":  "/",
	}
	if domain != "" {
		params["domain"] = domain
	}
	if rawURL != "" {
		params["url"] = rawURL
	}
	_, err := s.Do(ctx, "Network.setCookie", params)
	return err
}

func (s *Session) Close() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)
	}
	_ = s.ws.Close()
	if s.browser != nil && s.targetID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.browser.closeTab(ctx, s.targetID)
	}
	if s.tempDir != "" && s.targetID != "" {
		_ = os.Remove(filepath.Join(s.tempDir, fmt.Sprintf("page-%s.html", s.targetID)))
	}
}

func (s *Session) readLoop() {
	defer func() {
		s.mu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.mu.Unlock()
	}()
	for {
		var msg cdpMessage
		if err := s.ws.ReadJSON(&msg); err != nil {
			return
		}
		if msg.ID != 0 {
			s.mu.Lock()
			ch := s.pending[msg.ID]
			delete(s.pending, msg.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		select {
		case s.events <- msg:
		default:
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitPageWebSocket(ctx context.Context, port int) (string, error) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
		resp, err := client.Do(req)
		if err == nil {
			var pages []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			if json.NewDecoder(resp.Body).Decode(&pages) == nil {
				_ = resp.Body.Close()
				for _, p := range pages {
					if p.Type == "page" && p.WebSocketDebuggerURL != "" {
						return p.WebSocketDebuggerURL, nil
					}
				}
			} else {
				_ = resp.Body.Close()
			}
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", errors.New("Chrome DevTools 未就绪")
}

func fileURL(path string) string {
	abs, _ := filepath.Abs(path)
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
