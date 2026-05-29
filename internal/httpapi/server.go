package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"dd_screen_go/internal/browser"
	"dd_screen_go/internal/config"
	"dd_screen_go/internal/platform"
	"dd_screen_go/internal/render"
	"dd_screen_go/internal/storage"
	"dd_screen_go/internal/util"
)

type Dependencies struct {
	Config   config.Config
	Store    *storage.Store
	Browser  *browser.Browser
	Platform *platform.Service
	Render   *render.Renderer
}

type API struct {
	deps Dependencies

	verifyMu      sync.Mutex
	verifySession *browser.Session
}

type route struct {
	path     string
	disabled bool
	handler  http.HandlerFunc
}

func New(deps Dependencies) *API {
	return &API{deps: deps}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	routes := []route{
		{"/api/Bili/BiliQRCodeLogin", true, a.biliQRCodeLogin},
		{"/api/Bili/GetBiliCookie", true, a.getBiliCookie},
		{"/api/Bili/List", false, a.biliList},
		{"/api/Bili/Dynamic", false, a.biliDynamic},
		{"/api/Bili/Live", false, a.biliLive(false)},
		{"/api/Bili/Live2", false, a.biliLive(true)},
		{"/api/Bili/Live3", false, a.biliLive3},

		{"/api/Acfun/Dynamic", true, a.acfunDynamic},
		{"/api/Acfun/Live", true, a.acfunLive(false)},
		{"/api/Acfun/Live2", true, a.acfunLive(true)},
		{"/api/Douyin/DouyinQRCodeLogin", true, a.douyinQRCodeLogin},
		{"/api/Douyin/Live", true, a.douyinLive(false)},
		{"/api/Douyin/Live2", true, a.douyinLive(true)},
		{"/api/Douyin/Live3", false, a.douyinLive3},
		{"/api/Douyu/Live", true, a.douyuLive(false)},
		{"/api/Douyu/Live2", true, a.douyuLive(true)},
		{"/api/Huya/Live", true, a.huyaLive(false)},
		{"/api/Huya/Live2", true, a.huyaLive(true)},
		{"/api/Nico/Live", true, a.nicoLive(false)},
		{"/api/Nico/Live2", true, a.nicoLive(true)},
		{"/api/Twitch/Live", true, a.twitchLive(false)},
		{"/api/Twitch/Live2", true, a.twitchLive(true)},
		{"/api/Youtube/Card", true, a.youtubeCard},

		{"/api/Weibo/WeiboQRCodeLogin", true, a.weiboQRCodeLogin},
		{"/api/Weibo/RefreshWeiboCookie", true, a.refreshWeiboCookie},
		{"/api/Weibo/GetWeiboCookie", true, a.getWeiboCookie},
		{"/api/Weibo/GetMobileProfile", true, a.weiboMobileProfile},
		{"/api/Weibo/GetMobileCards", true, a.weiboMobileCards},
		{"/api/Weibo/Dynamic", true, a.weiboDynamic},

		{"/api/X/Dynamic", true, a.xDynamic},
		{"/api/X/NitterPoast", true, a.xNitter("https://nitter.poast.org")},
		{"/api/X/NitterNet", true, a.xNitter("https://nitter.net")},

		{"/api/XHH/GetTokenID", true, a.xhhGetTokenID},
		{"/api/XHH/GetDeviceID", true, a.xhhGetDeviceID},
		{"/api/XHH/GetSmidV2", true, a.xhhGetSmidV2},
		{"/api/XHH/GetProfileEvents", true, a.xhhProfileEvents(false)},
		{"/api/XHH/GetProfileEventsV2", true, a.xhhProfileEvents(true)},
		{"/api/XHH/Dynamic", true, a.xhhDynamic},
		{"/api/XHH/Verify", true, a.xhhVerify},
		{"/api/XHH/Verify/Screenshot", true, a.xhhVerifyScreenshot},
		{"/api/XHH/Verify/Click", true, a.xhhVerifyClick},
		{"/api/XHH/Verify/Close", true, a.xhhVerifyClose},
	}

	for _, rt := range routes {
		route := rt
		mux.HandleFunc(route.path, func(w http.ResponseWriter, r *http.Request) {
			if route.disabled && a.deps.Config.Debug {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			route.handler(w, r)
		})
	}

	mux.HandleFunc("/swagger/index.html", a.swaggerIndex)
	mux.HandleFunc("/swagger/v1/swagger.json", a.swaggerJSON)
	mux.Handle("/ScreenShotImg/", http.StripPrefix("/ScreenShotImg/", http.FileServer(http.Dir(a.deps.Store.ScreenshotDir))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/swagger" || r.URL.Path == "/swagger/" {
			http.Redirect(w, r, "/swagger/index.html", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/swagger/index.html", http.StatusFound)
	})
	return a.logging(mux)
}

func (a *API) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		
		if !strings.HasPrefix(r.URL.Path, "/swagger") && !strings.HasPrefix(r.URL.Path, "/ScreenShotImg") {
			util.Log("DBG", "HTTPAPI", "----> 收到请求: %s %s", r.Method, r.URL.RequestURI())
		}
		
		next.ServeHTTP(rec, r)
		if !strings.HasPrefix(r.URL.Path, "/swagger") && !strings.HasPrefix(r.URL.Path, "/ScreenShotImg") {
			util.LogRequest(r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (a *API) biliQRCodeLogin(w http.ResponseWriter, r *http.Request) {
	a.qrLogin(w, r, "bili", "https://passport.bilibili.com/login", `img[alt="Scan me!"], .qrcode-img img, img[src^="data:image"]`, "SESSDATA")
}

func (a *API) weiboQRCodeLogin(w http.ResponseWriter, r *http.Request) {
	a.qrLogin(w, r, "weibo", "https://passport.weibo.com/sso/signin?entry=miniblog&source=miniblog&disp=popup&url=https%3A%2F%2Fweibo.com%2Fnewlogin%3Ftabtype%3Dweibo%26gid%3D102803%26openLoginLayer%3D0%26url%3Dhttps%253A%252F%252Fweibo.com%252F&from=weibopro", `img[src*="qr.weibo.cn"], .qrcode img, img[src*="qrcode"], img[src^="data:image"]`, "SUB")
}

func (a *API) douyinQRCodeLogin(w http.ResponseWriter, r *http.Request) {
	a.qrLogin(w, r, "douyin", "https://www.douyin.com/", `#animate_qrcode_container img, img[src*="qr"], img[src^="data:image"], .login-panel img`, "sessionid")
}

func physicalClick(ctx context.Context, s *browser.Session, selector string) bool {
	rect, err := s.ElementRect(ctx, selector)
	if err != nil {
		return false
	}
	centerX := int(rect.X + rect.Width/2)
	centerY := int(rect.Y + rect.Height/2)
	
	// 如果是用户协议按钮，因为它是横向一整行文字，勾选框在最左侧，我们强制把点击点移到最左边 + 15px 处以确保精准点中复选框
	if strings.Contains(selector, "agree") {
		centerX = int(rect.X + 15)
	}
	
	err = s.Click(ctx, centerX, centerY)
	return err == nil
}

func (a *API) qrLogin(w http.ResponseWriter, r *http.Request, platformName, loginURL, selector, successCookie string) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s, err := a.deps.Browser.NewSession(ctx)
	if err != nil {
		util.WriteJSON(w, 500, fail(err))
		return
	}

	// Stealth scripting to bypass automated browser detection
	_, _ = s.Do(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `
Object.defineProperty(navigator, 'webdriver', { get: () => false });
window.navigator.chrome = { runtime: {} };
Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5] });
Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN','zh','en'] });
`})
	
	viewportWidth := 720
	viewportHeight := 720
	viewportScale := 2.0
	if platformName == "weibo" || platformName == "douyin" {
		viewportWidth = 1280
		viewportHeight = 1024
		viewportScale = 1.0
	}
	if err := s.SetViewport(ctx, viewportWidth, viewportHeight, viewportScale, false); err != nil {
		s.Close()
		util.WriteJSON(w, 500, fail(err))
		return
	}
	if err := s.Navigate(ctx, loginURL, 8*time.Second); err != nil {
		s.Close()
		util.WriteJSON(w, 500, fail(err))
		return
	}
	if platformName == "douyin" {
		time.Sleep(2 * time.Second)
		findJS := `(() => {
			const elements = Array.from(document.querySelectorAll('button, div, span, a, li'));
			const loginEl = elements.find(el => el.textContent.trim() === '登录' && el.offsetHeight > 0);
			if (loginEl) {
				let clickTarget = loginEl;
				let cur = loginEl;
				for (let i = 0; i < 4; i++) {
					if (!cur || cur.tagName === 'BODY') break;
					const className = cur.className || '';
					const style = window.getComputedStyle(cur);
					if (cur.tagName === 'A' || cur.tagName === 'BUTTON' || className.includes('item') || className.includes('btn') || style.cursor === 'pointer') {
						clickTarget = cur;
						break;
					}
					cur = cur.parentElement;
				}
				const old = document.querySelector('.dd-temp-douyin-login-btn');
				if (old) old.classList.remove('dd-temp-douyin-login-btn');
				
				clickTarget.classList.add('dd-temp-douyin-login-btn');
				return '.dd-temp-douyin-login-btn';
			}
			const loginClasses = document.querySelectorAll('[class*="login" i]');
			for (const el of loginClasses) {
				if (el.offsetHeight > 0 && (el.tagName === 'BUTTON' || el.tagName === 'A' || el.classList.contains('login-btn') || el.id.includes('login'))) {
					const old = document.querySelector('.dd-temp-douyin-login-btn');
					if (old) old.classList.remove('dd-temp-douyin-login-btn');
					el.classList.add('dd-temp-douyin-login-btn');
					return '.dd-temp-douyin-login-btn';
				}
			}
			return '';
		})()`

		checkLoginModalJS := `(() => {
			const qr = document.querySelector('#animate_qrcode_container img, img[src*="qr"], img[src^="data:image"], .login-panel img');
			if (qr && qr.offsetHeight > 0) return true;
			const textElements = Array.from(document.querySelectorAll('div, span, p'));
			return textElements.some(el => (el.textContent.includes('验证码') || el.textContent.includes('扫码') || el.textContent.includes('密码登录')) && el.offsetHeight > 0);
		})()`

		for i := 0; i < 8; i++ {
			resModal, errModal := s.Eval(ctx, checkLoginModalJS)
			var modalOpen bool
			if errModal == nil && json.Unmarshal(resModal, &modalOpen) == nil && modalOpen {
				util.Log("INF", "HTTPAPI", "成功检测到登录弹窗已打开")
				break
			}

			res, err := s.Eval(ctx, findJS)
			var sel string
			if err == nil && json.Unmarshal(res, &sel) == nil && sel != "" {
				if physicalClick(ctx, s, sel) {
					util.Log("INF", "HTTPAPI", "成功物理点击抖音登录按钮")
				}
			}
			time.Sleep(1500 * time.Millisecond)
		}
	}
	firstSelector := strings.Split(selector, ",")[0]
	if platformName == "douyin" {
		waitExpr := fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el) return false;
			const src = el.src || "";
			return src.startsWith("http") || src.startsWith("//");
		})()`, firstSelector)
		_ = s.WaitExpr(ctx, waitExpr, 15*time.Second)
	} else {
		_ = s.WaitExpr(ctx, fmt.Sprintf(`!!document.querySelector(%q) || !!document.querySelector(%q)`, firstSelector, selector), 15*time.Second)
	}
	
	var png []byte
	var loadedFromURL bool

	if platformName == "weibo" {
		srcRaw, srcErr := s.Eval(ctx, fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			return el ? el.src : "";
		})()`, firstSelector))
		var srcStr string
		if srcErr == nil && json.Unmarshal(srcRaw, &srcStr) == nil && strings.HasPrefix(srcStr, "http") {
			util.Log("INF", "HTTPAPI", "检测到微博二维码 URL: %s，开始直接下载...", srcStr)
			client := &http.Client{Timeout: 5 * time.Second}
			resp, httpErr := client.Get(srcStr)
			if httpErr == nil && resp.StatusCode == 200 {
				defer resp.Body.Close()
				if imgBytes, readErr := io.ReadAll(resp.Body); readErr == nil {
					png = imgBytes
					loadedFromURL = true
					util.Log("INF", "HTTPAPI", "微博二维码直接下载成功 (大小: %d 字节)", len(png))
				} else {
					util.Log("WRN", "HTTPAPI", "微博二维码读取字节流失败: %v", readErr)
				}
			} else {
				util.Log("WRN", "HTTPAPI", "直接下载微博二维码失败: %v", httpErr)
			}
		}
	}

	if !loadedFromURL {
		var err error
		png, err = s.Screenshot(ctx, firstSelector, false)
		if err != nil {
			png, err = s.Screenshot(ctx, "", false)
		}
		if err != nil {
			s.Close()
			util.WriteJSON(w, 500, fail(err))
			return
		}
	}

	go a.waitLoginCookies(platformName, successCookie, s)
	writePNG(w, png)
}

func (a *API) waitLoginCookies(platformName, successCookie string, s *browser.Session) {
	defer s.Close()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		cookies, err := s.Cookies(ctx)
		if err == nil && hasBrowserCookie(cookies, successCookie) {
			if platformName == "weibo" {
				mobileUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
				_, _ = s.Do(ctx, "Network.setUserAgentOverride", map[string]any{
					"userAgent": mobileUA,
				})
				_ = s.Navigate(ctx, "https://m.weibo.cn", 5*time.Second)
				time.Sleep(3 * time.Second)
				if refreshed, err := s.Cookies(ctx); err == nil {
					cookies = refreshed
				}
			}
			converted := make([]storage.Cookie, 0, len(cookies))
			for _, c := range cookies {
				converted = append(converted, storage.Cookie{
					Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
					Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: c.Expires,
				})
			}
			if err := a.deps.Store.WriteCookies(platformName, converted); err == nil {
				util.Log("INF", "HTTPAPI", "%s 扫码登录 Cookie 已保存", platformName)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func hasBrowserCookie(cookies []browser.Cookie, name string) bool {
	for _, c := range cookies {
		if strings.EqualFold(c.Name, name) && c.Value != "" {
			return true
		}
	}
	return false
}

func (a *API) getBiliCookie(w http.ResponseWriter, r *http.Request)  { a.cookieInfo(w, "bili") }
func (a *API) getWeiboCookie(w http.ResponseWriter, r *http.Request) { a.cookieInfo(w, "weibo") }

func (a *API) cookieInfo(w http.ResponseWriter, platformName string) {
	cookies, err := a.deps.Store.ReadCookies(platformName)
	if err != nil || len(cookies) == 0 {
		util.WriteJSON(w, 404, map[string]any{"success": false, "message": "Cookie文件不存在或为空，请先扫码登录"})
		return
	}
	type simple struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	out := make([]simple, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, simple{Name: c.Name, Value: c.Value})
	}
	util.WriteJSON(w, 200, map[string]any{"success": true, "count": len(out), "cookies": out})
}

func (a *API) biliList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bg := query(r, "bg")
	textChar := util.BoolQuery(query(r, "text_char"), false)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte(query(r, "form"))
	}
	png, err := a.deps.Render.SubscriptionList(ctx, string(body), bg, textChar)
	if err != nil {
		util.WriteJSON(w, 400, map[string]any{"code": -1, "msg": err.Error()})
		return
	}
	writePNG(w, png)
}

func (a *API) biliDynamic(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "动态地址不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	pic, err := a.deps.Render.SaveBiliDynamic(ctx, rawURL, util.BoolQuery(query(r, "column"), false), util.BoolQuery(query(r, "expand"), false), "dynamic")
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
}

func (a *API) acfunDynamic(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "文章地址不能为空"})
		return
	}
	if id := regexp.MustCompile(`ac(\d+)`).FindStringSubmatch(rawURL); len(id) > 1 {
		rawURL = "https://m.acfun.cn/v/?ac=" + id[1] + "&type=article&from=video"
	}
	a.screenshotJSON(w, r, "acfun_dynamic", rawURL, "", 1)
}

func (a *API) weiboDynamic(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "微博链接不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	pic, err := a.deps.Render.SaveWeiboDynamic(ctx, rawURL, "weibo_dynamic")
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
}

func (a *API) xDynamic(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "推文链接不能为空"})
		return
	}
	a.screenshotJSON(w, r, "x_tweet", rawURL, `article[data-testid="tweet"]`, 0)
}

func (a *API) xNitter(base string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawURL := query(r, "url")
		if rawURL == "" {
			util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "推文链接不能为空"})
			return
		}
		converted := convertNitterURL(rawURL, base)
		a.screenshotJSON(w, r, "x_nitter", converted, ".timeline-item, .main-tweet", 0)
	}
}

func (a *API) screenshotJSON(w http.ResponseWriter, r *http.Request, prefix, rawURL, selector string, view int) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	pic, err := a.deps.Render.SaveURLScreenshot(ctx, prefix, rawURL, selector, view)
	if err != nil && selector != "" {
		pic, err = a.deps.Render.SaveURLScreenshot(ctx, prefix, rawURL, "", view)
	}
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
}

func (a *API) biliLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "bili_live", query(r, "roomid"), other, a.deps.Platform.BiliLive)
	}
}

func (a *API) biliLive3(w http.ResponseWriter, r *http.Request) {
	a.liveEndpoint(w, r, "bili_live3", query(r, "roomid"), false, a.deps.Platform.BiliLive)
}

func (a *API) douyuLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "douyu_live", query(r, "roomid"), other, a.deps.Platform.DouyuLive)
	}
}

func (a *API) huyaLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "huya_live", query(r, "roomid"), other, a.deps.Platform.HuyaLive)
	}
}

func (a *API) acfunLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "acfun_live", query(r, "uid"), other, a.deps.Platform.ACFunLive)
	}
}

func (a *API) douyinLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "douyin_live", query(r, "roomid"), other, a.deps.Platform.DouyinLive)
	}
}

func (a *API) douyinLive3(w http.ResponseWriter, r *http.Request) {
	a.liveEndpoint(w, r, "douyin_live3", query(r, "roomid"), false, a.deps.Platform.DouyinLive)
}

func (a *API) nicoLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "nico_live", query(r, "liveId"), other, a.deps.Platform.NicoLive)
	}
}

func (a *API) twitchLive(other bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.liveEndpoint(w, r, "twitch_live", query(r, "login"), other, a.deps.Platform.TwitchLive)
	}
}

func (a *API) liveEndpoint(w http.ResponseWriter, r *http.Request, prefix, input string, other bool, fetch func(context.Context, string) (platform.LiveInfo, error)) {
	if input == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "房间号/链接不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	info, err := fetch(ctx, input)
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	opt := cardOptionsFromRequest(r)
	if strings.HasSuffix(r.URL.Path, "Live3") {
		opt.Variant = 3
	} else if other {
		opt.Variant = 2
	}
	pic, err := a.deps.Render.SaveLiveCard(ctx, prefix, info, opt)
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
}

func (a *API) youtubeCard(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "视频URL不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	info, err := a.deps.Platform.YoutubeCard(ctx, rawURL)
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	opt := cardOptionsFromRequest(r)
	opt.QR = util.BoolQuery(query(r, "qr"), true)
	opt.LiveState = util.IntQuery(query(r, "live_state"), 0)
	pic, err := a.deps.Render.SaveLiveCard(ctx, "youtube", info, opt)
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
}

func (a *API) refreshWeiboCookie(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := a.deps.Render.RefreshWeiboCookie(ctx)
	if err != nil {
		util.WriteJSON(w, 500, map[string]any{"success": false, "message": "刷新微博Cookie失败: " + err.Error()})
		return
	}
	util.WriteJSON(w, 200, map[string]any{"success": true, "message": "微博Cookie刷新成功"})
}

func (a *API) weiboMobileProfile(w http.ResponseWriter, r *http.Request) {
	a.rawJSONProxy(w, r, func(ctx context.Context) (string, error) {
		return a.deps.Platform.WeiboMobileProfile(ctx, query(r, "uid"))
	})
}

func (a *API) weiboMobileCards(w http.ResponseWriter, r *http.Request) {
	a.rawJSONProxy(w, r, func(ctx context.Context) (string, error) {
		return a.deps.Platform.WeiboMobileCards(ctx, query(r, "uid"))
	})
}

func (a *API) rawJSONProxy(w http.ResponseWriter, r *http.Request, fn func(context.Context) (string, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, err := fn(ctx)
	if err != nil {
		util.WriteJSON(w, 400, map[string]any{"success": false, "message": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

func (a *API) xhhGetTokenID(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, 200, map[string]any{"code": 0, "message": "成功", "data": "B" + platform.RandomDeviceID()})
}

func (a *API) xhhGetDeviceID(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, 200, map[string]any{"code": 0, "message": "成功", "data": platform.RandomDeviceID()})
}

func (a *API) xhhGetSmidV2(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, 200, map[string]any{"code": 0, "message": "成功", "data": platform.GenerateSmidV2()})
}

func (a *API) xhhProfileEvents(smid bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		token := query(r, "token")
		if smid {
			token = query(r, "smidV2")
		}
		text, err := a.deps.Platform.XHHProfileEvents(ctx, token, query(r, "userid"), query(r, "deviceId"), queryDefault(r, "device_info", "Chrome"), queryDefault(r, "UA", desktopUA), smid)
		if err != nil {
			util.WriteJSON(w, 200, fail(err))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(text))
	}
}

func (a *API) xhhDynamic(w http.ResponseWriter, r *http.Request) {
	rawURL := query(r, "url")
	if rawURL == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "url不能为空"})
		return
	}
	linkID := firstMatch(rawURL, `(?:link_id|linkId|linkid)[=/]([a-zA-Z0-9]+)`)
	if linkID != "" && query(r, "token") != "" && query(r, "deviceId") != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		text, err := a.deps.Platform.XHHTreeEvents(ctx, query(r, "token"), linkID, query(r, "deviceId"), queryDefault(r, "device_info", "Chrome"), queryDefault(r, "UA", desktopUA))
		if err == nil {
			pic, err := a.deps.Render.SaveXHHDynamic(ctx, "xhh_dynamic", text)
			if err != nil {
				util.WriteJSON(w, 200, fail(err))
				return
			}
			util.WriteJSON(w, 200, map[string]any{"code": 0, "pic": absoluteURL(r, pic)})
			return
		} else {
			util.WriteJSON(w, 200, fail(err))
			return
		}
	}
	util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "参数不足或无效的linkID"})
}

func (a *API) xhhVerify(w http.ResponseWriter, r *http.Request) {
	token := query(r, "token")
	if token == "" {
		util.WriteJSON(w, 200, map[string]any{"code": -1, "message": "token 不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s, err := a.deps.Browser.NewSession(ctx)
	if err != nil {
		util.WriteJSON(w, 200, fail(err))
		return
	}
	_ = s.SetViewport(ctx, 1440, 900, 1, false)
	_ = s.SetCookie(ctx, "x_xhh_tokenid", token, ".xiaoheihe.cn", "https://www.xiaoheihe.cn/")
	_ = s.Navigate(ctx, "https://www.xiaoheihe.cn/app/bbs/home", 8*time.Second)
	a.verifyMu.Lock()
	if a.verifySession != nil {
		a.verifySession.Close()
	}
	a.verifySession = s
	a.verifyMu.Unlock()
	util.WriteHTML(w, 200, remoteVerifyHTML)
}

func (a *API) xhhVerifyScreenshot(w http.ResponseWriter, r *http.Request) {
	a.verifyMu.Lock()
	s := a.verifySession
	a.verifyMu.Unlock()
	if s == nil {
		util.WriteJSON(w, 404, map[string]any{"message": "验证页面未打开"})
		return
	}
	png, err := s.Screenshot(r.Context(), "", false)
	if err != nil {
		util.WriteJSON(w, 500, map[string]any{"message": err.Error()})
		return
	}
	writePNG(w, png)
}

func (a *API) xhhVerifyClick(w http.ResponseWriter, r *http.Request) {
	a.verifyMu.Lock()
	s := a.verifySession
	a.verifyMu.Unlock()
	if s == nil {
		util.WriteJSON(w, 404, map[string]any{"message": "验证页面未打开"})
		return
	}
	err := s.Click(r.Context(), util.IntQuery(query(r, "x"), 0), util.IntQuery(query(r, "y"), 0))
	if err != nil {
		util.WriteJSON(w, 500, map[string]any{"message": err.Error()})
		return
	}
	util.WriteJSON(w, 200, map[string]any{"code": 0})
}

func (a *API) xhhVerifyClose(w http.ResponseWriter, r *http.Request) {
	a.verifyMu.Lock()
	if a.verifySession != nil {
		a.verifySession.Close()
		a.verifySession = nil
	}
	a.verifyMu.Unlock()
	util.WriteJSON(w, 200, map[string]any{"code": 0, "message": "验证页面已关闭"})
}

func cardOptionsFromRequest(r *http.Request) render.CardOptions {
	_ = r.ParseForm()
	return render.CardOptions{
		QR:         util.BoolQuery(query(r, "qr"), false),
		Content:    util.BoolQuery(query(r, "content"), true),
		View:       util.IntQuery(query(r, "view"), 0),
		ModelOrder: queryDefault(r, "model_order", "1,2,3,4"),
		Tips:       r.FormValue("tips"),
		Timestamp:  query(r, "timestamp"),
		LiveState:  util.IntQuery(query(r, "live_state"), 0),
		Standalone: util.BoolQuery(query(r, "standalone"), false),
	}
}

func writePNG(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func fail(err error) map[string]any {
	return map[string]any{"code": -1, "message": "发生错误：" + err.Error()}
}

func absoluteURL(r *http.Request, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = strings.Split(xf, ",")[0]
	}
	return scheme + "://" + r.Host + path
}

func query(r *http.Request, key string) string {
	_ = r.ParseForm()
	return strings.TrimSpace(r.FormValue(key))
}

func queryDefault(r *http.Request, key, def string) string {
	if v := query(r, key); v != "" {
		return v
	}
	return def
}

func firstMatch(s, pattern string) string {
	if m := regexp.MustCompile(`(?i)` + pattern).FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func convertNitterURL(rawURL, base string) string {
	m := regexp.MustCompile(`(?i)https?://(?:x|twitter)\.com/([^/]+)/status/(\d+)`).FindStringSubmatch(rawURL)
	if len(m) > 2 {
		return strings.TrimRight(base, "/") + "/" + m[1] + "/status/" + m[2]
	}
	return rawURL
}

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

func (a *API) swaggerIndex(w http.ResponseWriter, r *http.Request) {
	util.WriteHTML(w, 200, swaggerUIHTML)
}

func (a *API) swaggerJSON(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, 200, openAPIDocument(a.deps.Config.Debug))
}

const remoteVerifyHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>小黑盒 - 远程验证</title>
<style>body{margin:0;background:#172033;color:#eef3ff;font-family:"Microsoft YaHei",system-ui,sans-serif;display:flex;flex-direction:column;align-items:center}.toolbar{width:100%;box-sizing:border-box;padding:10px 16px;background:#22304a;display:flex;justify-content:space-between;align-items:center}.toolbar h1{font-size:15px;margin:0}.toolbar button{border:0;border-radius:4px;background:#e94560;color:#fff;padding:6px 12px;cursor:pointer}.screen{margin:12px;border:2px solid #304466;line-height:0;cursor:crosshair}.screen img{max-width:100vw;display:block}.status{font-size:12px;color:#aab4d3;padding:6px}</style></head><body>
<div class="toolbar"><h1>小黑盒 远程验证</h1><div><button onclick="refresh()">刷新截图</button> <button onclick="closePage()">关闭验证</button></div></div>
<div class="screen" id="wrap"><img id="img" src="/api/XHH/Verify/Screenshot"></div><div class="status" id="status">就绪</div>
<script>
const img=document.getElementById('img'),wrap=document.getElementById('wrap'),statusEl=document.getElementById('status');
function refresh(){img.src='/api/XHH/Verify/Screenshot?t='+Date.now()}
setInterval(refresh,1500);
wrap.onclick=async e=>{const r=img.getBoundingClientRect();const x=Math.round((e.clientX-r.left)*(img.naturalWidth/r.width));const y=Math.round((e.clientY-r.top)*(img.naturalHeight/r.height));statusEl.textContent='点击 '+x+','+y;await fetch('/api/XHH/Verify/Click?x='+x+'&y='+y,{method:'POST'});setTimeout(refresh,400)}
async function closePage(){await fetch('/api/XHH/Verify/Close',{method:'POST'});statusEl.textContent='验证页面已关闭';img.src=''}
</script></body></html>`
