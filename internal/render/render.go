package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dd_screen_go/internal/browser"
	"dd_screen_go/internal/platform"
	"dd_screen_go/internal/storage"
	"dd_screen_go/internal/util"

	qrcode "github.com/skip2/go-qrcode"
)

type Renderer struct {
	browser         *browser.Browser
	store           *storage.Store
	templateManager *TemplateManager
}

type CardOptions struct {
	QR         bool
	Content    bool
	View       int
	ModelOrder string
	Tips       string
	Timestamp  string
	LiveState  int
	Standalone bool
	Variant    int
}

func New(br *browser.Browser, store *storage.Store) *Renderer {
	return &Renderer{
		browser:         br,
		store:           store,
		templateManager: NewTemplateManager(),
	}
}

func (r *Renderer) TemplateManager() *TemplateManager {
	return r.templateManager
}

func (r *Renderer) LiveCard(ctx context.Context, prefix string, info platform.LiveInfo, opt CardOptions) ([]byte, error) {
	html, selector, variant := r.liveCardHTML(ctx, prefix, info, opt)
	png, err := r.captureLiveHTML(ctx, html, opt.View, selector, variant)
	if err != nil {
		return nil, err
	}
	return postProcessLivePNG(png, opt.View, variant)
}

func (r *Renderer) SaveLiveCard(ctx context.Context, prefix string, info platform.LiveInfo, opt CardOptions) (string, error) {
	png, err := r.LiveCard(ctx, prefix, info, opt)
	if err != nil {
		return "", err
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) URLScreenshot(ctx context.Context, rawURL, selector string, view int) ([]byte, error) {
	util.Log("DBG", "Render", "准备截取网页截图: %s (选择器: %s, 视图: %d)", rawURL, selector, view)
	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	if view == 1 {
		_ = s.SetViewport(ctx, 430, 920, 2, true)
	} else {
		_ = s.SetViewport(ctx, 1280, 1200, 1.5, false)
	}
	if err := s.Navigate(ctx, rawURL, 8*time.Second); err != nil {
		return nil, err
	}
	_ = s.WaitExpr(ctx, `Array.from(document.images || []).every(img => img.complete)`, 6*time.Second)
	if selector != "" {
		_ = s.WaitExpr(ctx, fmt.Sprintf(`!!document.querySelector(%q)`, selector), 8*time.Second)
	}
	return s.Screenshot(ctx, selector, selector == "")
}

func (r *Renderer) SaveURLScreenshot(ctx context.Context, prefix, rawURL, selector string, view int, variant int) (string, error) {
	png, err := r.URLScreenshot(ctx, rawURL, selector, view)
	if err != nil {
		return "", err
	}
	if r.templateManager.HasDynamicVariant(strings.Split(prefix, "_")[0], variant) {
		png, _ = r.WrapDynamicTemplate(ctx, png, variant, prefix, rawURL)
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) SaveBiliDynamic(ctx context.Context, rawURL string, column bool, expand bool, atCard bool, linkQr bool, prefix string, variant int) (string, error) {
	png, err := r.BiliDynamic(ctx, rawURL, column, expand, atCard, linkQr)
	if err != nil {
		return "", err
	}
	if r.templateManager.HasDynamicVariant(strings.Split(prefix, "_")[0], variant) {
		png, _ = r.WrapDynamicTemplate(ctx, png, variant, prefix, rawURL)
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) BiliDynamic(ctx context.Context, rawURL string, column bool, expand bool, atCard bool, linkQr bool) ([]byte, error) {
	id := biliDynamicID(rawURL)
	util.Log("DBG", "Render", "开始解析B站动态 | 原始URL: %s | 解析出ID: %s", rawURL, id)
	if id == "" {
		return nil, fmt.Errorf("动态地址不合法")
	}

	var lastErr error
	for _, target := range r.biliDynamicTargets(ctx, rawURL, id, column) {
		util.Log("DBG", "Render", "尝试B站动态目标提取方案: %s (选择器: %s)", target.URL, target.Selector)
		png, err := r.captureBiliDynamic(ctx, target, expand, atCard, linkQr)
		if err == nil {
			util.Log("DBG", "Render", "B站动态提取成功")
			return png, nil
		}
		util.Log("DBG", "Render", "提取失败: %v", err)
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("动态地址不合法")
}

func (r *Renderer) captureBiliDynamic(ctx context.Context, target biliDynamicTarget, expand bool, atCard bool, linkQr bool) ([]byte, error) {
	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	_, _ = s.Do(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `
Object.defineProperty(navigator, 'webdriver', { get: () => false });
window.navigator.chrome = { runtime: {} };
Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5] });
Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN','zh','en'] });
if (navigator.permissions && navigator.permissions.query) {
  const origQuery = navigator.permissions.query;
  navigator.permissions.query = (p) => p && p.name === 'notifications'
    ? Promise.resolve({ state: Notification.permission })
    : origQuery(p);
}
`})
	_, _ = s.Do(ctx, "Network.setBlockedURLs", map[string]any{"urls": []string{
		"*googletagmanager.com*",
		"*google-analytics.com*",
		"*doubleclick.net*",
		"*cm.bilibili.com*",
		"*beacon*",
		"*track*",
	}})
	_, _ = s.Do(ctx, "Network.setUserAgentOverride", map[string]any{
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36",
	})
	_, _ = s.Do(ctx, "Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{
		"Referer":            target.URL,
		"Accept-Language":    "zh-CN,zh;q=0.9,en;q=0.8",
		"Sec-Ch-Ua":          `"Google Chrome";v="148", "Chromium";v="148", "Not?A_Brand";v="24"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"Windows"`,
	}})
	_ = s.SetViewport(ctx, 2048, 2048, 2, false)

	if cookies, err := r.store.ReadCookies("bili"); err == nil {
		for _, c := range cookies {
			if c.Name != "" && c.Value != "" {
				_ = s.SetCookie(ctx, c.Name, c.Value, c.Domain, "https://www.bilibili.com/")
			}
		}
	}

	if err := s.Navigate(ctx, target.URL, 15*time.Second); err != nil {
		return nil, err
	}
	if err := s.WaitExpr(ctx, `!!document.body`, 10*time.Second); err != nil {
		return nil, err
	}
	var currentURL string
	if res, err := s.Eval(ctx, `window.location.href`); err == nil {
		_ = json.Unmarshal(res, &currentURL)
	}
	if strings.Contains(currentURL, "passport.bilibili.com") {
		return nil, fmt.Errorf("被重定向到登录页面")
	}
	_, _ = s.Eval(ctx, fmt.Sprintf(biliDynamicPrepareJS, biliDynamicPrepareCSS))

	waitExpr := fmt.Sprintf(`(() => {
		const t = document.title || '';
		if (t.includes('出错啦') || t.includes('404') || t.includes('412') || t.includes('验证码')) return true;
		const txt = document.body ? document.body.innerText : '';
		if (txt.includes('安全风控') || txt.includes('请求被拒绝')) return true;
		
		const el = document.querySelector(%q);
		if (!el) return false;
		const rect = el.getBoundingClientRect();
		return rect.width > 100 && rect.height > 50;
	})()`, target.Selector)

	if err := s.WaitExpr(ctx, waitExpr, 20*time.Second); err != nil {
		return nil, err
	}

	resRaw, _ := s.Eval(ctx, `(() => {
		const t = document.title || '';
		if (t.includes('出错啦') || t.includes('404') || t.includes('412') || t.includes('验证码')) return t;
		const txt = document.body ? document.body.innerText : '';
		if (txt.includes('安全风控') || txt.includes('请求被拒绝')) return '412风控拦截';
		return '';
	})()`)

	var errReason string
	if len(resRaw) > 0 {
		_ = json.Unmarshal(resRaw, &errReason)
	}
	if errReason != "" {
		return nil, fmt.Errorf("检测到错误页面，快速失败拦截 (原因: %s)", errReason)
	}

	// 先等待一次原始页面的所有图片加载完成，这能确保 Vue 已经把正文里的富文本元素（如 @标签）完全渲染到了 DOM 里。
	// 原来的顺序是页面刚拉起、刚找到容器的尺寸就立刻去查 @标签，那时候 Vue 可能还没把纯文本替换为 <a> 标签！
	_, _ = s.Eval(ctx, fmt.Sprintf(biliDynamicImagesReadyJS, target.Selector))

	if expand {
		util.Log("DBG", "Render", "执行图片强制展开...")
		expandResult, expandErr := s.Eval(ctx, biliDynamicExpandJS)
		if expandErr != nil {
			util.Log("WRN", "Render", "展开JS执行失败: %v", expandErr)
		} else {
			util.Log("DBG", "Render", "展开JS返回: %s", string(expandResult))
		}
	}

	if atCard || linkQr {
		util.Log("DBG", "Render", "注入 QR Code 库...")
		_, _ = s.Eval(ctx, biliDynamicQrCodeJs)
	}
	if atCard {
		util.Log("DBG", "Render", "注入用户卡片...")
		_, _ = s.Eval(ctx, biliDynamicAtCardJs)
	}
	if linkQr {
		util.Log("DBG", "Render", "注入链接二维码...")
		_, _ = s.Eval(ctx, biliDynamicLinkQrJs)
	}

	// 注入卡片后，可能会新增头像图片和二维码图片。再次等待这些新图片加载完成！
	_, _ = s.Eval(ctx, fmt.Sprintf(biliDynamicImagesReadyJS, target.Selector))

	_, _ = s.Eval(ctx, fmt.Sprintf(biliDynamicBeforeShotJS, target.Selector))

	_, _ = s.Eval(ctx, fmt.Sprintf(biliDynamicBeforeShotJS, target.Selector))

	buf, err := s.Screenshot(ctx, target.Selector, false)
	if err != nil {
		return nil, err
	}

	// Post-process to apply true transparent rounded corners in Go
	imgReader := bytes.NewReader(buf)
	decodedImg, _, err := image.Decode(imgReader)
	if err == nil {
		bounds := decodedImg.Bounds()
		w, h := bounds.Dx(), bounds.Dy()

		// Convert to NRGBA if needed
		nrgba, ok := decodedImg.(*image.NRGBA)
		if !ok {
			nrgba = image.NewNRGBA(bounds)
			draw.Draw(nrgba, bounds, decodedImg, bounds.Min, draw.Src)
		}

		// Calculate radius like C# (0.018 of short side, clamp 12-50)
		shortSide := w
		if h < w {
			shortSide = h
		}
		radiusFloat := float64(shortSide) * 0.018
		radius := int(math.Round(radiusFloat))
		if radius < 12 {
			radius = 12
		}
		if radius > 50 {
			radius = 50
		}

		applyRoundedCorners(nrgba, radius)

		var outBuf bytes.Buffer
		if err := png.Encode(&outBuf, nrgba); err == nil {
			return outBuf.Bytes(), nil
		}
	}

	return buf, nil
}

type biliDynamicTarget struct {
	URL      string
	Selector string
}

func (r *Renderer) biliDynamicTargets(ctx context.Context, rawURL, id string, column bool) []biliDynamicTarget {
	primaryURL := rawURL
	if !strings.HasPrefix(primaryURL, "http") {
		primaryURL = "https://t.bilibili.com/" + id
	}

	tURL := "https://t.bilibili.com/" + id
	opusURL := "https://www.bilibili.com/opus/" + id

	isOpus := r.biliDynamicIsOpusArticle(ctx, id, rawURL)
	if isOpus {
		primaryURL = opusURL
	} else {
		primaryURL = tURL
	}

	secondaryURL := tURL
	if primaryURL == tURL {
		secondaryURL = opusURL
	}

	tTarget := biliDynamicTarget{
		URL:      primaryURL,
		Selector: ".bili-opus-view, .bili-dyn-item",
	}
	opusTarget := biliDynamicTarget{
		URL:      secondaryURL,
		Selector: ".bili-opus-view, .bili-dyn-item",
	}

	return []biliDynamicTarget{tTarget, opusTarget}
}

func (r *Renderer) biliDynamicIsOpusArticle(ctx context.Context, id, referer string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?timezone_offset=-480&id="+id, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36")
	req.Header.Set("Referer", referer)
	if ck := r.store.CookieHeader("bili"); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var root map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&root); err != nil {
		return false
	}
	item := util.M(util.Get(util.M(root["data"]), "item"))
	return util.S(item["type"]) == "DYNAMIC_TYPE_ARTICLE"
}

func biliDynamicID(rawURL string) string {
	m := regexp.MustCompile(`^https://(?:t\.bilibili\.com/|www\.bilibili\.com/opus/)(\d+)(?:[?#].*)?$`).FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

const biliDynamicPrepareJS = `(() => {
const style = document.createElement('style');
style.textContent = %q;
document.head.appendChild(style);
return true;
})()`

const biliDynamicPrepareCSS = `
@font-face { font-family: 'PingFang SC'; src: local('Noto Sans CJK SC'), local('Noto Sans SC'), local('Source Han Sans SC'); }
@font-face { font-family: 'HarmonyOS_Regular'; src: local('Noto Sans CJK SC'), local('Noto Sans SC'), local('Source Han Sans SC'); }
@font-face { font-family: 'HarmonyOS Sans SC'; src: local('Noto Sans CJK SC'), local('Noto Sans SC'), local('Source Han Sans SC'); }
@font-face { font-family: 'Microsoft YaHei'; src: local('Noto Sans CJK SC'), local('Noto Sans SC'), local('Source Han Sans SC'); }
@font-face { font-family: 'Helvetica Neue'; src: local('Noto Sans CJK SC'), local('Noto Sans SC'), local('Source Han Sans SC'); }
* {
  font-family: 'Noto Sans CJK SC', 'Noto Sans SC', 'Noto Color Emoji', 'Apple Color Emoji', 'Segoe UI Emoji', sans-serif !important;
}
#bili-header-container, .bili-mini-content-wp, .v-popover-content,
.login-tip, .bili-dyn-item__more, .opus-module-author__more,
.opus-module-extend, .opus-module-bottom, .bili-popup,
.bili-app-footer, .bili-report-wrap, .bgc, .bg {
  display: none !important;
  visibility: hidden !important;
}
html, body {
  margin: 0 !important;
  padding: 0 !important;
  background: #f4f5f7 !important;
}
.__page, .__grid, .__tile { background: #ffffff !important; }
*, *::before, *::after {
  animation: none !important;
  transition: none !important;
}
`

const biliDynamicImagesReadyJS = `(async () => {
const node = document.querySelector(%q);
if (!node) return false;
const allImgs = Array.from(node.querySelectorAll('img'));
allImgs.forEach(function(img) {
  try { img.loading = 'eager'; } catch(_) {}
  try { img.decoding = 'sync'; } catch(_) {}
  try { img.removeAttribute('loading'); } catch(_) {}
  try { img.removeAttribute('onload'); } catch(_) {}
  try { img.removeAttribute('onerror'); } catch(_) {}
  try { img.removeAttribute('data-onload'); } catch(_) {}
  try { img.removeAttribute('data-onerror'); } catch(_) {}
  const bImg = img.closest('.b-img');
  if (bImg && bImg.classList.contains('sleepy')) bImg.classList.remove('sleepy');
  let mask = null;
  const picImg = img.closest('.bili-dyn-pic__img');
  if (picImg && picImg.parentElement) mask = picImg.parentElement.querySelector('.bili-dyn-pic__mask');
  if (mask) mask.style.display = 'none';
  let curSrc = img.getAttribute('src') || '';
  if (curSrc.startsWith('//')) img.src = 'https:' + curSrc;
  let lazySrc = img.getAttribute('data-src') || img.getAttribute('data-lazy-src') || '';
  if (lazySrc && (!img.src || img.src === 'about:blank' || img.src === window.location.href)) {
    if (lazySrc.startsWith('//')) lazySrc = 'https:' + lazySrc;
    img.src = lazySrc;
  }
});
const networkImgs = allImgs.filter(function(img) {
  if (img.complete && img.naturalWidth > 0) return false;
  const src = img.src || '';
  if (!src || src.startsWith('data:')) return false;
  return true;
});
if (!networkImgs.length) return true;
await Promise.race([
  Promise.all(networkImgs.map(function(img) {
    return new Promise(function(resolve) {
      if (img.complete) return resolve(true);
      const t = setTimeout(function() { resolve(false); }, 3500);
      img.onload = function() { clearTimeout(t); resolve(true); };
      img.onerror = function() { clearTimeout(t); resolve(false); };
    });
  })),
  new Promise(function(resolve) { setTimeout(resolve, 4500); })
]);
return true;
})()`

const biliDynamicRootReadyJS = `(() => {
const el = document.querySelector(%q);
if (!el) return false;
const rect = el.getBoundingClientRect();
return rect.width > 100 && rect.height > 50;
})()`

const biliDynamicBeforeShotJS = `(() => {
const el = document.querySelector(%q);
if (!el) return false;

// 修复单张竖图/窄图时右侧大面积空白的问题
el.style.setProperty('width', 'fit-content', 'important');
el.style.setProperty('max-width', '632px', 'important');
el.style.setProperty('min-width', '400px', 'important');
el.style.setProperty('margin', '0 auto', 'important');

const addBottom = el.matches('.bili-dyn-item');
const rect = el.getBoundingClientRect();
const newHeight = rect.height + (addBottom ? 20 : 0);
if (addBottom) el.style.height = newHeight + 'px';

el.style.cssText += 'margin: 0 !important; overflow: hidden !important; background: #ffffff !important; border: none !important; box-shadow: none !important;';

document.querySelectorAll('.bgc, .bg').forEach(function(e) {
  e.style.setProperty('display', 'none', 'important');
});
return true;
})()`

const biliDynamicExpandJS = `(async () => {
    var log = [];
    var expanded = 0;

    // ========== 策略1: viewpic spans ==========
    var spans = Array.from(document.querySelectorAll('span[data-type="viewpic"]'));
    log.push('viewpic spans: ' + spans.length);
    for (var s = 0; s < spans.length; s++) {
        var span = spans[s];
        var pics;
        try { pics = JSON.parse(span.getAttribute('data-pics') || '[]'); }
        catch (_) { continue; }
        if (!pics.length) continue;

        var container = document.createElement('div');
        container.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:10px;width:100%;margin:12px 0;';
        for (var p = 0; p < pics.length; p++) {
            var src = (pics[p].src || '').trim();
            if (!src) continue;
            if (src.startsWith('//')) src = 'https:' + src;
            if (src.startsWith('http://')) src = src.replace('http://', 'https://');
            var img = document.createElement('img');
            img.src = src;
            img.referrerPolicy = 'no-referrer';
            img.style.cssText = 'max-width:100%;width:auto;height:auto;display:block;border-radius:8px;object-fit:contain;';
            container.appendChild(img);
            expanded++;
        }
        span.parentNode.insertBefore(container, span.nextSibling);
        span.remove();
    }

    // ========== 策略2: 通用图片展开 (只在底部追加长图) ==========
    var root = document.querySelector('.bili-opus-view') || document.querySelector('.bili-dyn-item') || document;
    log.push('root: ' + (root === document ? 'document' : root.className));

    var containerSelectors = [
        '.dyn-card-opus__pics',
        '.bili-album__preview',
        '.bili-album__watch',
        '.bili-dyn-gallery',
        '.bili-dyn-pic__pics',
        '.horizontal-scroll-album',
        '.opus-module-top__album',
        '.bili-opus-view .opus-module-top'
    ];
    var gridContainers = new Set();
    for (var ci = 0; ci < containerSelectors.length; ci++) {
        var found = root.querySelectorAll(containerSelectors[ci]);
        for (var fi = 0; fi < found.length; fi++) {
            gridContainers.add(found[fi]);
        }
        if (found.length > 0) log.push('found ' + containerSelectors[ci] + ': ' + found.length);
    }

    var allImgs = Array.from(root.querySelectorAll('img'));
    var contentImgs = allImgs.filter(function(img) {
        var src = img.getAttribute('src') || img.getAttribute('data-src') || '';
        if (!src || src.startsWith('data:')) return false;
        var parent = img.closest('.opus-module-author, .bili-dyn-item__avatar, .bili-comment, .bili-avatar, .opus-module-bottom, .opus-module-extend');
        if (parent) return false;
        var inContent = img.closest('.opus-module-content, .dyn-card-opus, .bili-dyn-item__main, .bili-rich-text-module, .opus-module-top');
        return !!inContent;
    });
    log.push('content imgs: ' + contentImgs.length);

    if (contentImgs.length > 0 || gridContainers.size > 0) {
        contentImgs.forEach(function(img) {
            var p = img.parentElement;
            for (var d = 0; d < 8 && p && p !== root; d++) {
                var style = window.getComputedStyle(p);
                var display = style.display || '';
                if (display.includes('grid') || (display.includes('flex') && p.children.length > 1)) {
                    gridContainers.add(p);
                    break;
                }
                p = p.parentElement;
            }
        });
        log.push('grid containers: ' + gridContainers.size);

        var gridPromises = Array.from(gridContainers).map(async function(grid) {
            // 防止父子容器重复处理
            var isChild = false;
            var checkP = grid.parentElement;
            while(checkP && checkP !== root) {
                if (gridContainers.has(checkP)) { isChild = true; break; }
                checkP = checkP.parentElement;
            }
            if (isChild) return;

            var rawUrls = [];
            
            // 1. 提取 img 标签
            Array.from(grid.querySelectorAll('img')).forEach(function(img) {
                var src = img.getAttribute('src') || img.getAttribute('data-src') || '';
                if (src && !src.startsWith('data:')) rawUrls.push(src);
            });

            // 2. 提取带有 background-image 的元素 (B站相册缩略图)
            Array.from(grid.querySelectorAll('*')).forEach(function(el) {
                var bg = window.getComputedStyle(el).backgroundImage;
                if (bg && bg !== 'none' && bg.includes('url(')) {
                    var m = bg.match(/url\(['"]?(.*?)['"]?\)/);
                    if (m && m[1] && !m[1].startsWith('data:')) {
                        rawUrls.push(m[1]);
                    }
                }
            });

            // 清洗和去重 URL
            var urlMap = {};
            var cleanUrls = [];
            rawUrls.forEach(function(src) {
                if (src.startsWith('//')) src = 'https:' + src;
                if (src.startsWith('http://')) src = src.replace('http://', 'https://');
                var atIdx = src.indexOf('@');
                var baseSrc = atIdx !== -1 ? src.substring(0, atIdx) : src;
                
                // 提取文件名用于去重（忽略CDN域名的不同）
                var parts = baseSrc.split('/');
                var filename = parts[parts.length - 1];

                if (baseSrc && filename && !urlMap[filename]) {
                    urlMap[filename] = true;
                    cleanUrls.push(baseSrc);
                }
            });

            if (cleanUrls.length === 0) return;
            log.push('processing grid with ' + cleanUrls.length + ' unique imgs, class=' + (grid.className || 'none'));

            var imgUrls = cleanUrls;

            // 预加载原图，获取真实尺寸
            var preloaded = await Promise.all(imgUrls.map(function(src) {
                return new Promise(function(res) {
                    var im = new Image();
                    im.referrerPolicy = 'no-referrer';
                    var resolved = false;
                    var check = function() {
                        if (resolved) return;
                        if (im.naturalWidth > 0 && im.naturalHeight > 0) {
                            resolved = true;
                            res({ src: src, w: im.naturalWidth, h: im.naturalHeight });
                        }
                    };
                    var t = setInterval(check, 50);
                    var failT = setTimeout(function() {
                        if (!resolved) {
                            resolved = true;
                            clearInterval(t);
                            res({ src: src, w: 1, h: 1 });
                        }
                    }, 3000);
                    im.onload = function() { check(); if (!resolved) { resolved = true; clearInterval(t); clearTimeout(failT); res({ src: src, w: im.naturalWidth || 1, h: im.naturalHeight || 1 }); } };
                    im.onerror = function() { if (!resolved) { resolved = true; clearInterval(t); clearTimeout(failT); res({ src: src, w: 1, h: 1 }); } };
                    im.src = src;
                });
            }));

            // 过滤掉已经在全局展开过的图片，避免重复处理（针对多层嵌套或不同CDN域名）
            window.__globalExpandedUrls = window.__globalExpandedUrls || new Set();
            var actualPreloaded = [];
            preloaded.forEach(function(d) {
                var parts = d.src.split('/');
                var filename = parts[parts.length - 1];
                if (!window.__globalExpandedUrls.has(filename)) {
                    window.__globalExpandedUrls.add(filename);
                    actualPreloaded.push(d);
                }
            });

            if (actualPreloaded.length === 0) return;

            // 决定如何展开
            var imgsToExpand = [];
            var expandTitle = '';
            
            if (actualPreloaded.length === 1) {
                imgsToExpand = actualPreloaded.filter(function(d) {
                    return (d.h / d.w) >= 1.5; // 单张图只有是长图才展开
                });
                expandTitle = '👇 以下为长图展开 👇';
            } else if (actualPreloaded.length <= 3) {
                imgsToExpand = actualPreloaded; // 2~3张因为有裁剪所以全量展开
                expandTitle = '👇 以下为完整图片展开 👇';
            } else {
                imgsToExpand = actualPreloaded.filter(function(d) {
                    var ratio = d.h / d.w;
                    return ratio >= 1.5; // 高度是宽度的1.5倍及以上，视为长图
                });
                expandTitle = '👇 以下为长图展开 👇';
            }

            log.push('found ' + imgsToExpand.length + ' images to expand');

            if (imgsToExpand.length > 0) {
                // 原网格/相册保留不隐藏（恢复图四的样式）
                
                var newContainer = document.createElement('div');
                newContainer.style.cssText = 'display:flex;flex-direction:column;align-items:center;gap:12px;width:100%;margin:16px 0;border-top:1px dashed #e3e5e7;padding-top:16px;';
                
                var title = document.createElement('div');
                title.textContent = expandTitle;
                title.style.cssText = 'font-size:14px;color:#9499a0;margin-bottom:8px;font-weight:bold;';
                newContainer.appendChild(title);

                imgsToExpand.forEach(function(d) {
                    var newImg = document.createElement('img');
                    newImg.src = d.src;
                    newImg.referrerPolicy = 'no-referrer';
                    newImg.loading = 'eager';
                    newImg.decoding = 'sync';
                    newImg.style.cssText = 'max-width:100%;width:auto;height:auto;display:block;border-radius:8px;object-fit:contain;';
                    newContainer.appendChild(newImg);
                    expanded++;
                });

                // 决定插入的位置：尽量放在整篇内容的最后（文字下方）
                var insertTarget = null;
                if (root.classList.contains('bili-opus-view')) {
                    var contentNode = root.querySelector('.opus-module-content');
                    if (contentNode) insertTarget = contentNode;
                } else if (root.classList.contains('bili-dyn-item')) {
                    var mainNode = root.querySelector('.bili-dyn-item__main');
                    if (mainNode) insertTarget = mainNode;
                }

                if (insertTarget) {
                    insertTarget.appendChild(newContainer);
                } else {
                    grid.parentNode.insertBefore(newContainer, grid.nextSibling);
                }
            }
        });

        await Promise.all(gridPromises);
    }

    log.push('total expanded: ' + expanded);
    return log.join(' | ');
})()`

func (r *Renderer) SubscriptionList(ctx context.Context, rawJSON, bg string, textChar bool) ([]byte, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &data); err != nil {
		return nil, fmt.Errorf("JSON 数据不合法: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("暂无任何订阅")
	}
	html := subscriptionListHTML(groupSubscriptionData(data), bg, textChar)
	return r.captureHTML(ctx, html, 0, "")
}

func (r *Renderer) captureHTML(ctx context.Context, html string, view int, selector string) ([]byte, error) {
	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	if view == 1 {
		_ = s.SetViewport(ctx, 430, 960, 2.5, true)
	} else {
		_ = s.SetViewport(ctx, 1180, 100, 2, false)
	}
	if err := s.SetContent(ctx, html); err != nil {
		return nil, err
	}
	_ = s.WaitExpr(ctx, `document.fonts ? document.fonts.ready.then(() => true) : true`, 5*time.Second)
	_ = s.WaitExpr(ctx, `Array.from(document.images || []).every(img => img.complete)`, 8*time.Second)
	return s.Screenshot(ctx, selector, selector == "")
}

func (r *Renderer) captureLiveHTML(ctx context.Context, html string, view int, selector string, variant int, injectJS ...string) ([]byte, error) {
	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	switch {
	case variant == 3:
		_ = s.SetViewport(ctx, 1080, 1080, 2, false)
	case variant == 2 && view != 0:
		_ = s.SetViewport(ctx, 430, 932, 4, false)
	case variant == 2:
		_ = s.SetViewport(ctx, 785, 1042, 2, false)
	case view != 0:
		_ = s.SetViewport(ctx, 390, 844, 3, false)
	default:
		_ = s.SetViewport(ctx, 785, 1042, 2, false)
	}
	if err := s.SetContent(ctx, html); err != nil {
		return nil, err
	}
	for _, js := range injectJS {
		_, _ = s.Eval(ctx, js)
	}
	_ = s.WaitExpr(ctx, `document.fonts ? document.fonts.ready.then(() => true) : true`, 6*time.Second)
	_ = s.WaitExpr(ctx, liveCardReadyJS, 18*time.Second)
	return s.Screenshot(ctx, selector, selector == "")
}

func (r *Renderer) liveCardHTML(ctx context.Context, prefix string, info platform.LiveInfo, opt CardOptions) (string, string, int) {
	isLive := opt.LiveState == 0
	mode := "live"
	if !isLive {
		mode = "off"
	}

	cover := firstNonEmpty(info.Cover, info.Avatar)
	coverData := imageDataURL(ctx, cover, originalNoCover)
	avatarData := imageDataURL(ctx, firstNonEmpty(info.Avatar, cover), coverData)
	title := firstNonEmpty(info.Title, "直播间")
	nickname := firstNonEmpty(info.Nickname, info.Author, info.RoomID, info.Platform)
	liveURL := firstNonEmpty(info.LiveURL, info.SourceURL, info.RoomID)
	if opt.Standalone && strings.EqualFold(info.Platform, "Bilibili") && info.RoomID != "" {
		liveURL = "https://www.bilibili.com/blackboard/live/live-activity-player.html?enterTheRoom=0&cid=" + info.RoomID
	}
	areas := strings.TrimSpace(info.Category)
	durationLabel := "直播时长："
	duration := ""
	if !isLive {
		duration = calculateLiveDuration(info.StartTime, opt.Timestamp)
		if duration == "" && opt.Timestamp != "" {
			duration = formatSinceUnixTimestamp(opt.Timestamp)
		}
	}

	variant := opt.Variant
	qr := ""
	if opt.QR && liveURL != "" {
		if variant == 2 {
			qr = qrDataURLTransparent(liveURL)
		} else {
			qr = qrDataURLPlain(liveURL)
		}
	}

	platform := strings.Split(prefix, "_")[0]
	if r.templateManager.HasLiveVariant(platform, variant) {
		data := LiveTemplateData{
			Info:          info,
			AvatarBase64:  avatarData,
			CoverBase64:   coverData,
			QRBase64:      qr,
			Duration:      duration,
			DurationLabel: durationLabel,
			IsLive:        isLive,
			Timestamp:     opt.Timestamp,
			Options:       opt,
		}
		if tHTML, err := r.templateManager.RenderLiveVariant(platform, variant, data); err == nil {
			return tHTML, "", variant
		} else {
			util.Log("ERR", "Render", "Failed to render custom live template for variant %d (Platform: %s): %v", variant, platform, err)
		}
	}
	if variant == 3 {
		return r.liveCuteHTML(ctx, opt, info, coverData, avatarData, title, nickname, areas, qr, duration, durationLabel), "body", 3
	}
	if variant == 2 {
		return liveOtherHTML(opt, mode, isLive, coverData, avatarData, title, nickname, areas, qr, duration, durationLabel), ".card", 2
	}
	return livePosterHTML(opt, info.Platform, isLive, coverData, avatarData, title, nickname, info.Description, qr, duration, durationLabel), ".canvas", 1
}

func (r *Renderer) liveCuteHTML(ctx context.Context, opt CardOptions, info platform.LiveInfo, coverData, avatarData, title, nickname, areas, qr, duration, durationLabel string) string {
	htmlStr := CuteCardTemplateHTML

	liveStatus := 0
	if opt.LiveState == 0 {
		liveStatus = 1
	}

	followerStr := "0"
	if info.FollowerNum >= 10000 {
		followerStr = fmt.Sprintf("%.1f万", float64(info.FollowerNum)/10000.0)
		followerStr = strings.TrimSuffix(followerStr, ".0")
	} else if info.FollowerNum > 0 {
		followerStr = fmt.Sprintf("%d", info.FollowerNum)
	}

	guardStr := fmt.Sprintf("%d", info.GuardNum)

	timeLabel := ""
	timeVal := ""
	if liveStatus == 1 {
		timeLabel = "开播时间"
		if len(info.StartTime) > 16 {
			timeVal = info.StartTime[:16]
		} else {
			timeVal = info.StartTime
		}
		if timeVal == "" {
			timeVal = time.Now().Format("2006-01-02 15:04")
		}
	} else {
		if duration != "" {
			if durationLabel == "下播时间：" {
				timeLabel = "下播时间"
			} else {
				timeLabel = "本场时长"
			}
			timeVal = duration
		} else {
			timeLabel = "本场时长"
			timeVal = "已结束"
		}
	}

	// 替换结构化的 Jinja 逻辑块
	if liveStatus == 1 {
		htmlStr = strings.ReplaceAll(htmlStr, `class="{% if live_status != 1 %}is-offline{% endif %}"`, `class=""`)
		htmlStr = strings.ReplaceAll(htmlStr, `class="live-badge {% if live_status != 1 %}is-offline{% endif %}"`, `class="live-badge"`)
	} else {
		htmlStr = strings.ReplaceAll(htmlStr, `class="{% if live_status != 1 %}is-offline{% endif %}"`, `class="is-offline"`)
		htmlStr = strings.ReplaceAll(htmlStr, `class="live-badge {% if live_status != 1 %}is-offline{% endif %}"`, `class="live-badge is-offline"`)
	}

	if coverData != "" {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`+"\r\n"+`                <img src="{{ cover_url }}" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`+"\r\n"+`            {% else %}`+"\r\n"+`                <div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`+"\r\n"+`            {% endif %}`,
			fmt.Sprintf(`<img src="%s" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`, coverData))
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`+"\n"+`                <img src="{{ cover_url }}" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`+"\n"+`            {% else %}`+"\n"+`                <div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`+"\n"+`            {% endif %}`,
			fmt.Sprintf(`<img src="%s" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`, coverData))
	} else {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`+"\r\n"+`                <img src="{{ cover_url }}" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`+"\r\n"+`            {% else %}`+"\r\n"+`                <div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`+"\r\n"+`            {% endif %}`,
			`<div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`)
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`+"\n"+`                <img src="{{ cover_url }}" class="cover-image" id="coverImg" alt="Cover" crossorigin="anonymous">`+"\n"+`            {% else %}`+"\n"+`                <div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`+"\n"+`            {% endif %}`,
			`<div style="width:100%;height:100%;background:#e1e1e1;display:flex;align-items:center;justify-content:center;color:#aaa;font-size:40px;">NO COVER</div>`)
	}

	if liveStatus == 1 {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if live_status == 1 %}正在直播{% else %}直播已结束{% endif %}`, "正在直播")
	} else {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if live_status == 1 %}正在直播{% else %}直播已结束{% endif %}`, "直播已结束")
	}

	timePillContent := fmt.Sprintf(`<span>%s</span>
                    <span style="opacity: 0.7; margin: 0 8px;">|</span>
                    <span>%s</span>`, timeLabel, timeVal)

	targetTimeBlockRN := `            {% if live_status == 1 or live_status == 0 %}
            <div class="time-pill">
                {% if live_status == 1 %}
                    <span>开播时间</span>
                    <span style="opacity: 0.7; margin: 0 8px;">|</span>
                    <!-- 修正：只使用后端传入的 safe 函数 -->
                    <span>{{ getTime(live_time, "") }}</span>
                {% else %}
                    <span>本场时长</span>
                    <span style="opacity: 0.7; margin: 0 8px;">|</span>
                    <span>{{ getTime(live_time, "elapsed") }}</span>
                {% endif %}
            </div>
            {% endif %}`
	targetTimeBlockN := strings.ReplaceAll(targetTimeBlockRN, "\r\n", "\n")
	htmlStr = strings.ReplaceAll(htmlStr, targetTimeBlockRN, fmt.Sprintf(`            <div class="time-pill">
                %s
            </div>`, timePillContent))
	htmlStr = strings.ReplaceAll(htmlStr, targetTimeBlockN, fmt.Sprintf(`            <div class="time-pill">
                %s
            </div>`, timePillContent))

	if avatarData != "" {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if face_url %}`+"\r\n"+`            <img src="{{ face_url }}" class="avatar-img">`+"\r\n"+`            {% else %}`+"\r\n"+`            <img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`+"\r\n"+`            {% endif %}`,
			fmt.Sprintf(`<img src="%s" class="avatar-img">`, avatarData))
		htmlStr = strings.ReplaceAll(htmlStr, `{% if face_url %}`+"\n"+`            <img src="{{ face_url }}" class="avatar-img">`+"\n"+`            {% else %}`+"\n"+`            <img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`+"\n"+`            {% endif %}`,
			fmt.Sprintf(`<img src="%s" class="avatar-img">`, avatarData))
	} else {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if face_url %}`+"\r\n"+`            <img src="{{ face_url }}" class="avatar-img">`+"\r\n"+`            {% else %}`+"\r\n"+`            <img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`+"\r\n"+`            {% endif %}`,
			`<img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`)
		htmlStr = strings.ReplaceAll(htmlStr, `{% if face_url %}`+"\n"+`            <img src="{{ face_url }}" class="avatar-img">`+"\n"+`            {% else %}`+"\n"+`            <img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`+"\n"+`            {% endif %}`,
			`<img src="https://i0.hdslb.com/bfs/face/member/noface.jpg" class="avatar-img">`)
	}

	if coverData != "" {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`, "")
		htmlStr = strings.ReplaceAll(htmlStr, `{% endif %}`, "")
	} else {
		htmlStr = strings.ReplaceAll(htmlStr, `{% if cover_url %}`, "<!--")
		htmlStr = strings.ReplaceAll(htmlStr, `{% endif %}`, "-->")
	}

	statsHTML := ""
	if info.Platform == "Douyin" {
		userCountStr := info.UserCount
		if userCountStr == "" {
			userCountStr = "-"
		}
		statsHTML = fmt.Sprintf(`            <div class="stat-item">
                <div class="stat-label">在线观看</div>
                <div class="stat-val text-pink">%s</div>
            </div>`, userCountStr)
	} else {
		statsHTML = fmt.Sprintf(`            <div class="stat-item">
                <div class="stat-label">粉丝</div>
                <div class="stat-val">%s</div>
            </div>
            <div class="divider"></div>
            <div class="stat-item">
                <div class="stat-label">粉丝牌</div>
                <div class="stat-val text-pink">%s</div>
            </div>
            <div class="divider"></div>
            <div class="stat-item">
                <div class="stat-label">舰长</div>
                <div class="stat-val text-blue">%s</div>
            </div>`, followerStr, firstNonEmpty(info.MedalName, "-"), guardStr)
	}

	areaHTML := ""
	if areas != "" && info.Platform != "Douyin" {
		areaHTML = fmt.Sprintf(`<div class="area-subtitle">%s</div>`, areas)
	}

	replacer := strings.NewReplacer(
		"{{ theme_primary or '#FF7EB3' }}", "#FF7EB3",
		"{{ theme_primary_light or '#FFC2D1' }}", "#FFC2D1",
		"{{ theme_primary_dark or '#FF5E83' }}", "#FF5E83",
		"{{ theme_secondary or '#7EC2FF' }}", "#7EC2FF",
		"{{ cover_url }}", coverData,
		"{{ title }}", title,
		"{{ area_html }}", areaHTML,
		"{{ stats_html }}", statsHTML,
		"{{ uname }}", nickname,
		"{{ description if description else title }}", firstNonEmpty(stripHTMLTags(info.Description), title),
		`{% if tips %}`, "",
		`{% endif %}`, "",
		`{{ tips }}`, opt.Tips,
	)
	htmlStr = replacer.Replace(htmlStr)

	return htmlStr
}

func liveOtherHTML(opt CardOptions, mode string, isLive bool, coverData, avatarData, title, nickname, areas, qr, duration, durationLabel string) string {
	order := parseOrder(opt.ModelOrder)
	if len(order) == 0 {
		order = []int{1, 2, 3}
	}

	var body strings.Builder
	for _, code := range order {
		switch code {
		case 1:
			body.WriteString(`<div class="cover"><div class="badge"><span class="dot" aria-hidden="true"></span><span class="badge-text">直播已结束</span></div>`)
			if !isLive && duration != "" {
				body.WriteString(fmt.Sprintf(`<div class="duration">%s%s</div>`, util.Escape(durationLabel), util.Escape(duration)))
			}
			body.WriteString(fmt.Sprintf(`<img id="cover" alt="封面" src="%s" /></div>`, util.Escape(coverData)))
		case 2:
			body.WriteString(fmt.Sprintf(`<div class="left"><h2 class="title">%s</h2><div class="meta"><div class="avatar-wrap"><div class="avatar"><img id="avatar" alt="头像" src="%s" /></div></div><div class="text"><div class="up">%s</div>`,
				util.Escape(title), util.Escape(avatarData), util.Escape(nickname)))
			if areas != "" {
				body.WriteString(fmt.Sprintf(`<div class="actions"><a href="#">%s</a></div>`, util.Escape(areas)))
			}
			body.WriteString(`</div></div></div>`)
		case 3:
			if isLive && opt.QR && qr != "" {
				body.WriteString(fmt.Sprintf(`<div class="qr"><div class="qr-tip">扫描二维码进入直播间~</div><div class="side"><img id="qr" alt="二维码" src="%s" /></div></div>`, util.Escape(qr)))
			}
		}
	}

	body.WriteString(fmt.Sprintf(`<div class="signature">MIAOYUAPI x DDBOT%s</div>`, util.Escape(opt.Tips)))
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>直播卡片</title>
<style>%s</style>
</head>
<body class="%s">
<div class="wrap"><div class="card layout-a">%s</div></div>
<script>%s</script>
</body>
</html>`, originalStyleCSS, mode, body.String(), originalScriptJS)
}

func livePosterHTML(opt CardOptions, platformName string, isLive bool, coverData, avatarData, title, nickname, description, qr, duration, durationLabel string) string {
	isBili := strings.EqualFold(platformName, "Bilibili")
	order := parseOrder(opt.ModelOrder)
	if len(order) == 0 {
		if isBili {
			order = []int{1, 2, 3, 4}
		} else {
			order = []int{1, 2, 3}
		}
	}

	var sections strings.Builder
	for _, code := range order {
		switch code {
		case 1:
			sections.WriteString(fmt.Sprintf(`<section class="mod mod-hero"><div class="hero"><div class="right"><figure class="banner-card"><img src="%s" alt="活动横幅" />`, util.Escape(coverData)))
			if !isLive && duration != "" {
				sections.WriteString(fmt.Sprintf(`<div class="banner-ribbon" aria-label="本场直播时长"><span class="ribbon-txt">%s%s</span></div>`, util.Escape(durationLabel), util.Escape(duration)))
			}
			sections.WriteString(`</figure></div></div></section>`)
		case 2:
			sections.WriteString(fmt.Sprintf(`<section class="mod mod-info"><div class="info-card card-glass"><div class="name-row"><div class="name-avatar"><img src="%s" alt="avatar" /></div><div class="nickname">%s</div><div class="live-badge"><span class="dot"></span><span>占位文字</span></div></div><div class="headline">%s</div></div></section>`,
				util.Escape(avatarData), util.Escape(nickname), util.Escape(title)))
		case 3:
			if isBili {
				if opt.Content && strings.TrimSpace(stripHTMLTags(description)) != "" {
					sections.WriteString(fmt.Sprintf(`<section class="mod mod-details"><div class="details card-glass">%s</div></section>`, util.Escape(stripHTMLTags(description))))
				}
			} else {
				if isLive && opt.QR && qr != "" {
					sections.WriteString(fmt.Sprintf(`<section class="mod mod-meta"><div class="meta card-glass"><div class="qr"><img src="%s" alt="扫码二维码" /></div><div class="meta-txt"><div class="cta" contenteditable="true">扫码进入直播间~</div><div class="time">时间：%s</div></div></div></section>`,
						util.Escape(qr), time.Now().Format("2006-01-02 15:04")))
				}
			}
		case 4:
			if isBili {
				if isLive && opt.QR && qr != "" {
					sections.WriteString(fmt.Sprintf(`<section class="mod mod-meta"><div class="meta card-glass"><div class="qr"><img src="%s" alt="扫码二维码" /></div><div class="meta-txt"><div class="cta" contenteditable="true">扫码进入直播间~</div><div class="time">时间：%s</div></div></div></section>`,
						util.Escape(qr), time.Now().Format("2006-01-02 15:04")))
				}
			}
		}
	}

	css := originalLiveCSS
	if !isLive {
		css = originalOffLiveCSS
	}
	background := strings.ReplaceAll(coverData, `'`, `\'`)
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
<title>直播海报</title>
<style>%s</style>
<style id="__canvas_bg">.canvas::before{background-image:url('%s') !important;}</style>
</head>
<body>
<div class="canvas"><div class="stack">%s<section class="mod mod-credits"><div class="credits">MIAOYUAPI × DDBOT%s</div></section></div></div>
</body>
</html>`, css, background, sections.String(), util.Escape(opt.Tips))
}

func imageDataURL(ctx context.Context, rawURL, fallback string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fallback
	}
	if strings.HasPrefix(rawURL, "data:") {
		return rawURL
	}
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return fallback
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fallback
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36")
	req.Header.Set("Referer", rawURL)
	req.Header.Set("Accept", "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="148", "Chromium";v="148", "Not?A_Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || len(data) == 0 {
		return fallback
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" || !strings.HasPrefix(contentType, "image/") {
		contentType = "image/png"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func formatSinceUnixTimestamp(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return ""
	}
	var raw int64
	if _, err := fmt.Sscanf(ts, "%d", &raw); err != nil || raw <= 0 {
		return ""
	}
	if len(ts) == 10 {
		raw *= 1000
	}
	delta := time.Now().UnixMilli() - raw
	if delta < 0 {
		return ""
	}
	minutes := int((delta / int64(time.Minute/time.Millisecond)) % 60)
	hours := int(delta / int64(time.Hour/time.Millisecond))
	if minutes == 0 {
		minutes = 1
	}
	if hours < 1 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
}

func calculateLiveDuration(startTimeStr string, endTimeStr string) string {
	startTimeStr = strings.TrimSpace(startTimeStr)
	endTimeStr = strings.TrimSpace(endTimeStr)
	if startTimeStr == "" {
		return ""
	}

	loc, _ := time.LoadLocation("Local")
	if loc == nil {
		loc = time.Local
	}
	startTime, err := time.ParseInLocation("2006-01-02 15:04:05", startTimeStr, loc)
	if err != nil {
		startTime, err = time.Parse("2006-01-02 15:04:05", startTimeStr)
		if err != nil {
			return ""
		}
	}

	var endTime time.Time
	if endTimeStr != "" {
		if ts, err := strconv.ParseInt(endTimeStr, 10, 64); err == nil {
			if ts > 9999999999 {
				ts = ts / 1000
			}
			endTime = time.Unix(ts, 0)
		}
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}

	delta := endTime.Sub(startTime)
	if delta < 0 {
		return ""
	}

	minutes := int(delta.Minutes()) % 60
	hours := int(delta.Hours())
	if minutes == 0 && hours == 0 {
		minutes = 1
	}

	if hours < 1 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
}

func postProcessLivePNG(data []byte, view int, variant int) ([]byte, error) {
	if variant == 3 {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return data, nil
	}

	x, y, cropW, cropH := 0, 0, w, h
	if variant == 2 {
		x = 2
		cropW = w - 4
		if view != 0 {
			y = 7
			cropH = h - 11
		} else {
			y = 5
			cropH = h - 8
		}
	} else if view != 0 {
		x = 2
		y = 4
		cropW = w - 4
		cropH = h - 8
	} else {
		y = 2
		cropH = h - 4
	}
	if cropW <= 0 || cropH <= 0 {
		return data, nil
	}

	out := image.NewNRGBA(image.Rect(0, 0, cropW, cropH))
	draw.Draw(out, out.Bounds(), img, image.Point{X: bounds.Min.X + x, Y: bounds.Min.Y + y}, draw.Src)
	radius := 28
	if view != 0 {
		radius = 70
	}
	applyRoundedCorners(out, radius)

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func applyRoundedCorners(img *image.NRGBA, radius int) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	if radius <= 0 || w <= 0 || h <= 0 {
		return
	}
	if radius*2 > w {
		radius = w / 2
	}
	if radius*2 > h {
		radius = h / 2
	}
	r := float64(radius)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy float64
			inCorner := false
			switch {
			case x < radius:
				dx = float64(radius) - 0.5 - float64(x)
				inCorner = true
			case x >= w-radius:
				dx = float64(x) - (float64(w-radius) - 0.5)
				inCorner = true
			}
			switch {
			case y < radius:
				dy = float64(radius) - 0.5 - float64(y)
				inCorner = true
			case y >= h-radius:
				dy = float64(y) - (float64(h-radius) - 0.5)
				inCorner = true
			}
			if !inCorner || dx == 0 || dy == 0 {
				continue
			}
			dist := math.Hypot(dx, dy)
			if dist <= r-1 {
				continue
			}
			off := img.PixOffset(x, y)
			if dist >= r {
				img.Pix[off+3] = 0
				continue
			}
			img.Pix[off+3] = uint8(float64(img.Pix[off+3]) * (r - dist))
		}
	}
}

const liveCardReadyJS = `(async () => {
const imgs = Array.from(document.querySelectorAll('img'));
for (const img of imgs) {
  try {
    img.loading = 'eager';
    img.decoding = 'sync';
    try { img.fetchPriority = 'high'; } catch (_) {}
  } catch (_) {}
}
const waitOne = (img) => new Promise(resolve => {
  if (img.complete && img.naturalWidth > 0) return resolve(true);
  const done = () => {
    img.removeEventListener('load', done);
    img.removeEventListener('error', done);
    resolve(true);
  };
  img.addEventListener('load', done, { once: true });
  img.addEventListener('error', done, { once: true });
  setTimeout(() => resolve(false), 5000);
});
const waitForCSSBackground = () => new Promise(resolve => {
  const hasCanvas = !!document.querySelector('.canvas');
  const bodyBefore = getComputedStyle(document.body, '::before').backgroundImage;
  const coverSrc = getComputedStyle(document.body).getPropertyValue('--cover-src').trim();
  const needsWait = (coverSrc && coverSrc !== 'none') || 
                    (bodyBefore && bodyBefore !== 'none' && bodyBefore !== '') || 
                    hasCanvas;
  if (!needsWait) return resolve(true);

  let attempts = 0;
  const check = () => {
    attempts++;
    const curCoverSrc = getComputedStyle(document.body).getPropertyValue('--cover-src').trim();
    const curBodyBefore = getComputedStyle(document.body, '::before').backgroundImage;
    const canvas = document.querySelector('.canvas');
    const curCanvasBefore = canvas ? getComputedStyle(canvas, '::before').backgroundImage : 'none';
    if ((curCoverSrc && curCoverSrc !== 'none' && curCoverSrc.includes('url')) ||
        (curBodyBefore && curBodyBefore !== 'none') ||
        (curCanvasBefore && curCanvasBefore !== 'none')) {
      return setTimeout(() => resolve(true), 400);
    }
    if (attempts >= 15) return resolve(false);
    setTimeout(check, 200);
  };
  check();
});
await Promise.race([
  Promise.all([Promise.all(imgs.map(waitOne)), waitForCSSBackground(), new Promise(r => setTimeout(r, 400))]),
  new Promise(r => setTimeout(r, 15000))
]);
await new Promise(r => setTimeout(r, 400));
return true;
})()`

func detailsSection(info platform.LiveInfo) string {
	parts := []string{}
	for _, item := range []string{info.Category, info.Description, info.UserCount} {
		if strings.TrimSpace(item) != "" {
			parts = append(parts, util.Escape(item))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return `<section class="mod mod-details"><div class="details card-glass">` + strings.Join(parts, " · ") + `</div></section>`
}

func qrSection(qr, status, timestamp string) string {
	cta := "扫码观看直播"
	if strings.Contains(status, "结束") {
		cta = "扫码查看直播间"
	}
	if strings.Contains(status, "视频") {
		cta = "扫码观看视频"
	}
	return fmt.Sprintf(`<section class="mod mod-meta"><div class="meta card-glass"><div class="qr"><img src="%s" alt="二维码"></div><div class="meta-txt"><div class="cta">%s</div><div class="time">%s</div></div></div></section>`, qr, cta, util.Escape(timestamp))
}

func imageTag(src, alt string) string {
	if src == "" {
		return `<div class="image-placeholder"></div>`
	}
	return fmt.Sprintf(`<img src="%s" alt="%s" referrerpolicy="no-referrer">`, util.Escape(src), util.Escape(alt))
}

func qrDataURLTransparent(text string) string {
	return qrDataURL(text, qrcode.High, color.RGBA{R: 169, G: 169, B: 169, A: 255}, color.RGBA{A: 0}, true)
}

func qrDataURLPlain(text string) string {
	return qrDataURL(text, qrcode.High, color.Black, color.White, false)
}

func qrDataURL(text string, level qrcode.RecoveryLevel, foreground, background color.Color, disableBorder bool) string {
	qr, err := qrcode.New(text, level)
	if err != nil {
		return ""
	}
	qr.ForegroundColor = foreground
	qr.BackgroundColor = background
	qr.DisableBorder = disableBorder
	png, err := qr.PNG(-20)
	if err != nil {
		return ""
	}
	if disableBorder {
		if trimmed, ok := trimTransparentBorderPNG(png); ok {
			png = trimmed
		}
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func trimTransparentBorderPNG(data []byte) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return nil, false
	}
	if minX == b.Min.X && minY == b.Min.Y && maxX == b.Max.X-1 && maxY == b.Max.Y-1 {
		return data, true
	}
	crop := image.Rect(0, 0, maxX-minX+1, maxY-minY+1)
	dst := image.NewRGBA(crop)
	draw.Draw(dst, crop, img, image.Point{X: minX, Y: minY}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func parseOrder(s string) []int {
	if s == "" {
		return nil
	}
	seen := map[int]bool{}
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var n int
		_, _ = fmt.Sscanf(part, "%d", &n)
		if n >= 1 && n <= 4 && !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func cardCSS() string {
	return `
*{box-sizing:border-box}html,body{margin:0;min-height:100%;font-family:"Microsoft YaHei","PingFang SC",system-ui,sans-serif;background:#fff;color:#182033}
body{display:grid;place-items:center;padding:24px}.canvas{width:min(980px,94vw);border-radius:24px;overflow:hidden;box-shadow:0 22px 60px rgba(34,52,92,.18);background:linear-gradient(135deg,#f9fbff,#eef6ff 55%,#fff7ec)}
body.off .canvas{background:linear-gradient(135deg,#1b2132,#273149 55%,#171b29);color:#edf3ff}
.stack{display:flex;flex-direction:column;gap:18px;padding:28px}.card-glass{border-radius:18px;background:rgba(255,255,255,.84);box-shadow:0 10px 28px rgba(31,66,146,.12);backdrop-filter:blur(10px)}
body.off .card-glass{background:rgba(22,28,44,.72);box-shadow:0 10px 28px rgba(0,0,0,.28)}
.banner-card{position:relative;margin:0;aspect-ratio:16/9;border-radius:22px;overflow:hidden;background:#dfe7f5;box-shadow:0 18px 36px rgba(25,45,95,.16)}
.banner-card img{width:100%;height:100%;object-fit:cover;display:block}.image-placeholder{width:100%;height:100%;background:linear-gradient(135deg,#c9d8ef,#f1d8d0)}
.banner-ribbon{position:absolute;right:14px;top:14px;padding:8px 12px;border-radius:12px;background:rgba(0,0,0,.55);color:#fff;font-weight:700;font-size:14px}
.name-row{display:flex;align-items:center;gap:14px;padding:16px 20px 10px}.name-avatar{width:78px;height:78px;border-radius:50%;overflow:hidden;flex:0 0 auto;background:#e8eef8}.name-avatar img{width:100%;height:100%;object-fit:cover}
.nickname{font-size:30px;font-weight:800;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.live-badge{margin-left:auto;display:flex;align-items:center;gap:8px;padding:7px 13px;border-radius:999px;background:#fff2df;color:#8d4b00;font-weight:800;white-space:nowrap}
body.off .live-badge{background:rgba(255,255,255,.1);color:#dde6ff}.dot{width:10px;height:10px;border-radius:50%;background:#ff8a00;box-shadow:0 0 0 4px rgba(255,138,0,.18)}body.off .dot{background:#8f97ad;box-shadow:0 0 0 4px rgba(143,151,173,.18)}
.headline{font-size:24px;font-weight:800;line-height:1.35;text-align:center;padding:0 22px 18px}.details{padding:18px 22px;text-align:center;line-height:1.7;color:#5c6984}body.off .details{color:#c7d1ee}
.meta{display:grid;grid-template-columns:auto 1fr;gap:18px;align-items:center;padding:16px 20px}.qr{width:138px;height:138px;border-radius:16px;overflow:hidden;background:#fff}.qr img{width:100%;height:100%}.cta{font-size:20px;font-weight:800}.time{margin-top:8px;color:#697692}body.off .time{color:#aeb9d6}
.credits{text-align:center;color:#7e8fb6;letter-spacing:2px;font-size:14px}.mod:empty{display:none}
@media(max-width:560px){body{padding:10px}.stack{padding:16px;gap:14px}.name-row{flex-wrap:wrap}.nickname{font-size:24px;flex:1}.live-badge{margin-left:0}.headline{font-size:20px}.meta{grid-template-columns:1fr;text-align:center}.qr{margin:auto}}
`
}

type groupedData map[string]map[string][]subscriptionUser

type subscriptionUser struct {
	Name      string
	UID       string
	Pic       string
	WatchType string
}

func groupSubscriptionData(raw map[string]any) groupedData {
	out := groupedData{}
	for platformName, platformValue := range raw {
		platform := util.M(platformValue)
		if platform == nil {
			continue
		}
		ids := util.A(platform["Ids"])
		if len(ids) == 0 {
			continue
		}
		if out[platformName] == nil {
			out[platformName] = map[string][]subscriptionUser{}
		}
		for _, item := range ids {
			m := util.M(item)
			if m == nil {
				continue
			}
			name := util.S(m["Name"])
			uid := util.S(m["Uid"])
			u := subscriptionUser{
				Name:      name,
				UID:       uid,
				Pic:       util.S(m["Pic"]),
				WatchType: strings.ReplaceAll(strings.ReplaceAll(util.S(m["WatchType"]), "live", "直播"), "news", "动态"),
			}
			group := "#"
			if name != "" {
				r := []rune(name)[0]
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					group = strings.ToUpper(string(r))
				}
			}
			out[platformName][group] = append(out[platformName][group], u)
		}
	}
	for _, groups := range out {
		for key := range groups {
			sort.Slice(groups[key], func(i, j int) bool {
				return groups[key][i].Name < groups[key][j].Name
			})
		}
	}
	return out
}

func subscriptionListHTML(data groupedData, bg string, textChar bool) string {
	platforms := make([]string, 0, len(data))
	for p := range data {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	var body strings.Builder
	for _, p := range platforms {
		groups := data[p]
		total := 0
		for _, users := range groups {
			total += len(users)
		}
		body.WriteString(fmt.Sprintf(`<div class="platform"><h2>%s <span>%d</span></h2><div class="user-list">`, util.Escape(p), total))
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, u := range groups[k] {
				pic := ""
				if u.Pic != "" {
					pic = fmt.Sprintf(`<img src="%s" referrerpolicy="no-referrer">`, util.Escape(u.Pic))
				}
				nameClass := "name"
				if textChar {
					nameClass += " ellipsis"
				}

				uidText := ""
				if u.UID != "" {
					uidStr := u.UID
					if !strings.HasPrefix(strings.ToUpper(uidStr), "UID") && !strings.Contains(uidStr, "(") {
						uidStr = "UID: " + uidStr
					}
					uidText = fmt.Sprintf(`<div class="uid">%s</div>`, util.Escape(uidStr))
				}

				body.WriteString(fmt.Sprintf(`<div class="user">
<div class="user-info">
%s
<div class="name-uid">
<div class="%s">%s</div>
%s
</div>
</div>
<div class="user-meta">
<div class="letter">%s</div>
<div class="watch-type">%s</div>
</div>
</div>`, pic, nameClass, util.Escape(u.Name), uidText, util.Escape(k), util.Escape(u.WatchType)))
			}
		}
		body.WriteString(`</div></div>`)
	}
	bgCSS := ""
	if strings.HasPrefix(bg, "http://") || strings.HasPrefix(bg, "https://") || strings.HasPrefix(bg, "data:") {
		bgCSS = fmt.Sprintf(`background-image:linear-gradient(rgba(255,255,255,0.9),rgba(255,255,255,0.9)),url(%q);background-size:cover;background-position:center;`, bg)
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;font-family:"Microsoft YaHei",Arial,sans-serif;background:#ffffff;color:#333;width:fit-content;min-width:100%%;%s}
.wrap{padding:60px 80px;display:flex;flex-direction:column;align-items:center;box-sizing:border-box;width:fit-content;min-width:100%%}
.title{text-align:center;font-size:32px;color:#555;margin-bottom:60px}
.cols{display:flex;flex-wrap:nowrap;gap:100px;justify-content:center;align-items:flex-start}
.platform{display:flex;flex-direction:column;min-width:280px}
h2{font-size:54px;font-style:italic;color:#f39c12;margin:0 0 30px 0;display:flex;justify-content:flex-start;align-items:baseline;gap:15px;font-weight:800}
h2 span{font-size:28px;color:#e74c3c;font-style:italic;font-weight:600}
.user-list{display:flex;flex-direction:column;gap:30px}
.user{display:flex;justify-content:space-between;align-items:center;gap:40px}
.user-info{display:flex;align-items:center;gap:15px}
.user-info img{width:48px;height:48px;border-radius:50%%;object-fit:cover;box-shadow:0 2px 8px rgba(0,0,0,0.1)}
.name-uid{display:flex;flex-direction:column}
.name{font-size:18px;font-weight:bold;color:#333}
.uid{font-size:14px;color:#888;margin-top:4px}
.ellipsis{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:200px}
.user-meta{display:flex;flex-direction:column;align-items:center;gap:8px}
.letter{font-size:32px;font-style:italic;color:#2ecc71;line-height:1;font-weight:800}
.watch-type{font-size:15px;color:#3498db;white-space:nowrap}
.footer{text-align:center;margin-top:80px;color:#aaa;font-size:16px}
</style></head><body><div class="wrap"><div class="title">DDBOT 订阅列表</div><div class="cols">%s</div><div class="footer">%s</div></div></body></html>`, bgCSS, body.String(), time.Now().Format("2006-01-02 15:04:05"))
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	return html.UnescapeString(strings.TrimSpace(s))
}

func (r *Renderer) RefreshWeiboCookie(ctx context.Context) error {
	cookies, err := r.store.ReadCookies("weibo")
	if err != nil || len(cookies) == 0 {
		return fmt.Errorf("Cookie文件不存在或为空，无法刷新")
	}

	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	_, _ = s.Do(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `
Object.defineProperty(navigator, 'webdriver', { get: () => false });
window.navigator.chrome = { runtime: {} };
Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5] });
Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN','zh','en'] });
`})

	mobileUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
	_, _ = s.Do(ctx, "Network.setUserAgentOverride", map[string]any{
		"userAgent": mobileUA,
	})

	for _, c := range cookies {
		if c.Name != "" && c.Value != "" {
			cookieURL := "https://weibo.com/"
			if strings.Contains(c.Domain, "weibo.cn") {
				cookieURL = "https://weibo.cn/"
			}
			_ = s.SetCookie(ctx, c.Name, c.Value, c.Domain, cookieURL)
		}
	}

	if err := s.Navigate(ctx, "https://m.weibo.cn", 15*time.Second); err != nil {
		return err
	}

	time.Sleep(3 * time.Second)

	refreshed, err := s.Cookies(ctx)
	if err != nil {
		return err
	}

	var mobileCookies []storage.Cookie
	for _, c := range refreshed {
		if strings.Contains(c.Domain, "weibo.cn") || strings.Contains(c.Domain, "m.weibo.cn") {
			mobileCookies = append(mobileCookies, storage.Cookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: c.Expires,
			})
		}
	}

	if len(mobileCookies) == 0 {
		return fmt.Errorf("刷新失败，未获取到移动端Cookie")
	}

	return r.store.WriteCookies("weibo", mobileCookies)
}

func (r *Renderer) SaveWeiboDynamic(ctx context.Context, rawURL string, prefix string, variant int) (string, error) {
	png, err := r.WeiboDynamic(ctx, rawURL)
	if err != nil {
		return "", err
	}
	if r.templateManager.HasDynamicVariant(strings.Split(prefix, "_")[0], variant) {
		png, _ = r.WrapDynamicTemplate(ctx, png, variant, prefix, rawURL)
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) WeiboDynamic(ctx context.Context, rawURL string) ([]byte, error) {
	matches := regexp.MustCompile(`https?://weibo\.com/(\d+)/([A-Za-z0-9]+)`).FindStringSubmatch(rawURL)
	if len(matches) < 3 {
		return nil, fmt.Errorf("微博地址不合法")
	}
	uid := matches[1]
	mid := matches[2]
	targetURL := fmt.Sprintf("https://weibo.com/%s/%s", uid, mid)

	s, err := r.browser.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	// 1. Stealth scripting
	_, _ = s.Do(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": `
Object.defineProperty(navigator, 'webdriver', { get: () => false });
window.navigator.chrome = { runtime: {} };
Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5] });
Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN','zh','en'] });
`})

	// 2. Extra headers
	_, _ = s.Do(ctx, "Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{
		"Referer":         "https://weibo.com/",
		"Accept-Language": "zh-CN,zh;q=0.9",
	}})

	// 3. User-Agent
	_, _ = s.Do(ctx, "Network.setUserAgentOverride", map[string]any{
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36",
	})

	// 4. Set cookies from storage
	if cookies, err := r.store.ReadCookies("weibo"); err == nil {
		for _, c := range cookies {
			if c.Name != "" && c.Value != "" {
				cookieURL := "https://weibo.com/"
				if strings.Contains(c.Domain, "weibo.cn") {
					cookieURL = "https://weibo.cn/"
				}
				_ = s.SetCookie(ctx, c.Name, c.Value, c.Domain, cookieURL)
			}
		}
	}

	// 5. Set large viewport (2048x2048 with scale 2)
	_ = s.SetViewport(ctx, 2048, 2048, 2, false)

	// 6. Navigate
	if err := s.Navigate(ctx, targetURL, 15*time.Second); err != nil {
		return nil, err
	}

	// 7. Wait for article, annotate it, and wait for all images to complete loading
	waitJS := fmt.Sprintf(`(() => {
		const targetUid = %q;
		return new Promise((resolve, reject) => {
			let attempts = 0;
			let prevImgCount = 0;
			let stableCount = 0;
			
			const findAndLoad = () => {
				attempts++;
				let article = null;
				const link = document.querySelector("a[href*='/u/" + targetUid + "']");
				if (link) {
					article = link.closest('article');
				}
				if (!article) {
					const usercard = document.querySelector('[usercard*="id=' + targetUid + '"], [action-data*="uid=' + targetUid + '"]');
					if (usercard) {
						article = usercard.closest('article');
					}
				}
				if (!article) {
					// Fallback: on a detail page, the first article is the main post
					article = document.querySelector('article');
				}
				
				if (!article) {
					if (attempts > 40) {
						reject("找不到博主链接或微博卡片，请确认是否登录成功");
					} else {
						setTimeout(findAndLoad, 500);
					}
					return;
				}
				
				// Wait for DOM to stabilize (number of images doesn't change for 3 consecutive checks)
				const currentImgs = article.querySelectorAll('img');
				if (currentImgs.length !== prevImgCount) {
					prevImgCount = currentImgs.length;
					stableCount = 0;
					setTimeout(findAndLoad, 100);
					return;
				}
				if (stableCount < 3) {
					stableCount++;
					setTimeout(findAndLoad, 100);
					return;
				}
				
				// Mark the body
				const body = article.querySelector('div[class*="_body_"]');
				if (body) {
					body.setAttribute('data-dd-weibo-card-body', '1');
					body.style.setProperty('padding-bottom', '20px', 'important');
				} else {
					article.setAttribute('data-dd-weibo-card-body', '1');
					article.style.setProperty('padding-bottom', '20px', 'important');
				}
				
				// Remove follow button
				const followBtn = article.querySelector('button[class*="_followbtn_"]');
				if (followBtn) {
					const container = followBtn.closest('div.woo-box-flex');
					if (container) container.remove();
				}
				
				// Scroll into view to trigger lazy loading
				article.scrollIntoView({ block: 'center' });
				window.dispatchEvent(new Event('scroll'));
				
				// Force eager load
				const imgs = Array.from(article.querySelectorAll('img'));
				for (const img of imgs) {
					if (img.loading) img.loading = 'eager';
					img.decoding = 'sync';
				}
				
				// Wait for all images to be fully loaded
				const waitImg = (img) => new Promise(res => {
					let checks = 0;
					const check = () => {
						checks++;
						const src = img.src || '';
						const isNetworkImg = src.startsWith('http') || src.startsWith('//');
						if (isNetworkImg) {
							if (img.complete && img.naturalWidth > 0) {
								return res();
							}
							img.addEventListener('load', () => res(), { once: true });
							img.addEventListener('error', () => res(), { once: true });
							if (img.complete && img.naturalWidth > 0) {
								return res();
							}
							return;
						}
						// If it's a data-URI placeholder, wait up to 3 seconds for it to be swapped to a network URL
						if (checks > 30) {
							return res();
						}
						setTimeout(check, 100);
					};
					check();
				});
				
				Promise.all(imgs.map(waitImg)).then(() => {
					// Extra 1.2s sleep for layout rendering to stabilize (like C# master)
					setTimeout(() => resolve(true), 1200);
				});
			};
			
			findAndLoad();
		});
	})()`, uid)

	if _, err := s.Eval(ctx, waitJS); err != nil {
		return nil, fmt.Errorf("微博内容模块就绪超时，请确保已成功扫码登录并且地址正确")
	}

	// 8. Screenshot the element
	return s.Screenshot(ctx, "[data-dd-weibo-card-body='1']", false)
}

func (r *Renderer) WrapDynamicTemplate(ctx context.Context, png []byte, variant int, prefix string, rawURL string) ([]byte, error) {
	platform := strings.Split(prefix, "_")[0]
	data := DynamicTemplateData{
		ImageBase64: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		RawURL:      rawURL,
	}
	tHTML, err := r.templateManager.RenderDynamicVariant(platform, variant, data)
	if err != nil {
		return nil, err
	}
	return r.captureLiveHTML(ctx, tHTML, 0, "", variant)
}

func (r *Renderer) FetchBiliDynamicRaw(ctx context.Context, id string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?timezone_offset=-480&id="+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	cookies, _ := r.store.ReadCookies("bili")
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("JSON parse error: %v, body: %s", err, string(body))
	}
	return data, nil
}

// fetchBiliVoteInfo fetches vote options from the VC API using vote_id.
// Returns the vote info map (with options array) or nil on failure.
func (r *Renderer) fetchBiliVoteInfo(ctx context.Context, voteID int64) map[string]any {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("https://api.vc.bilibili.com/vote_svr/v1/vote_svr/vote_info?vote_id=%d", voteID), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	cookies, _ := r.store.ReadCookies("bili")
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		util.Log("WRN", "VoteInfo", "vote_id=%d error=%v", voteID, err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	if code, ok := root["code"].(float64); ok && code != 0 {
		util.Log("WRN", "VoteInfo", "vote_id=%d code=%.0f", voteID, code)
		return nil
	}
	vdata := util.M(root["data"])
	if vdata == nil {
		return nil
	}
	info := util.M(vdata["info"])
	if info == nil {
		return nil
	}
	util.Log("DBG", "VoteInfo", "vote_id=%d options=%v", voteID, info["options"])
	return info
}

// fetchBiliUserInfo fetches user profile info from B站 API (with cookies).
// Returns the user info map (face, name, sign, etc.) or nil on failure.
func (r *Renderer) fetchBiliUserInfo(ctx context.Context, mid string) map[string]any {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.bilibili.com/x/space/app/index?mid="+mid, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://space.bilibili.com/"+mid)
	cookies, _ := r.store.ReadCookies("bili")
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		util.Log("WRN", "AT-Fetch", "mid=%s error=%v", mid, err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	if code, ok := root["code"].(float64); ok && code != 0 {
		util.Log("WRN", "AT-Fetch", "mid=%s code=%.0f", mid, code)
		return nil
	}
	data := util.M(root["data"])
	if data == nil {
		return nil
	}
	info := util.M(data["info"])
	if info == nil {
		return nil
	}
	util.Log("DBG", "AT-Fetch", "mid=%s name=%q", mid, util.S(info["name"]))
	return info
}

func ParseBiliDynamic(rawData map[string]any, dynamicID string) *BiliDynamicSimple {
	if rawData == nil {
		return nil
	}

	data := util.M(rawData["data"])
	var item map[string]any
	if data != nil {
		item = util.M(data["item"])
	}

	res := &BiliDynamicSimple{}
	if data == nil || item == nil {
		if dynamicID != "" {
			fetchAndParseOpus(dynamicID, res)
			if res.AuthorName != "" || res.Text != "" || res.Title != "" {
				return res
			}
		}
		return nil
	}

	res = parseBiliDynamicItem(item)
	if res != nil && res.Text == "" && res.Title == "" && dynamicID != "" {
		fetchAndParseOpus(dynamicID, res)
	}
	return res
}

func fetchAndParseOpus(id string, res *BiliDynamicSimple) {
	req, err := http.NewRequest("GET", "https://www.bilibili.com/opus/"+id, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	htmlStr := string(body)
	jsonStr := extractInitialStateJSON(htmlStr)
	if jsonStr == "" {
		return
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return
	}

	detail := util.M(data["detail"])
	if detail == nil {
		return
	}
	modules := util.A(detail["modules"])
	if modules == nil {
		return
	}

	for _, m := range modules {
		mod := util.M(m)
		if mod == nil {
			continue
		}
		moduleType := util.S(mod["module_type"])
		if moduleType == "MODULE_TYPE_AUTHOR" {
			authorMap := util.M(mod["module_author"])
			if authorMap != nil {
				res.AuthorName = util.S(authorMap["name"])
				res.AuthorAvatar = util.S(authorMap["face"])
				res.PubTime = util.S(authorMap["pub_time"])
			}
		} else if moduleType == "MODULE_TYPE_TOP" {
			topMap := util.M(mod["module_top"])
			if topMap != nil {
				display := util.M(topMap["display"])
				if display != nil {
					album := util.M(display["album"])
					if album != nil {
						pics := util.A(album["pics"])
						for _, p := range pics {
							pic := util.M(p)
							if pic != nil {
								res.Images = append(res.Images, util.S(pic["url"]))
							}
						}
					}
				}
			}
		} else if moduleType == "MODULE_TYPE_TITLE" {
			titleMap := util.M(mod["module_title"])
			if titleMap != nil {
				res.Title = util.S(titleMap["text"])
			}
		} else if moduleType == "MODULE_TYPE_CONTENT" {
			contentMap := util.M(mod["module_content"])
			if contentMap != nil {
				paragraphs := util.A(contentMap["paragraphs"])
				for _, p := range paragraphs {
					para := util.M(p)
					if para == nil {
						continue
					}
					// Text nodes
					textMap := util.M(para["text"])
					if textMap != nil {
						nodes := util.A(textMap["nodes"])
						for ni, n := range nodes {
							nMap := util.M(n)
							if nMap == nil {
								continue
							}
							nodeType := util.S(nMap["type"])
							if nodeType == "TEXT_NODE_TYPE_WORD" {
								word := util.M(nMap["word"])
								if word != nil {
									text := util.S(word["words"])
									// 如果下一个节点是 WEB，去掉文本末尾的空白/换行（保留开头换行用于正常换行）
									nextIsWEB := false
									if ni+1 < len(nodes) {
										if nextN := util.M(nodes[ni+1]); nextN != nil {
											if util.S(nextN["type"]) == "TEXT_NODE_TYPE_RICH" {
												if nextRich := util.M(nextN["rich"]); nextRich != nil {
													if util.S(nextRich["type"]) == "RICH_TEXT_NODE_TYPE_WEB" {
														nextIsWEB = true
													}
												}
											}
										}
									}
									if nextIsWEB {
										text = strings.TrimRight(text, " \n\r\t")
									}
									res.Text += text
									res.RichText = append(res.RichText, BiliRichTextNode{
										Type: "RICH_TEXT_NODE_TYPE_TEXT",
										Text: text,
									})
								}
							} else if nodeType == "TEXT_NODE_TYPE_RICH" {
								rich := util.M(nMap["rich"])
								if rich != nil {
									res.Text += util.S(rich["text"])
									richNode := BiliRichTextNode{
										Type: util.S(rich["type"]),
										Text: util.S(rich["text"]),
									}
									if util.S(rich["type"]) == "RICH_TEXT_NODE_TYPE_WEB" {
										richNode.JumpURL = util.S(rich["jump_url"])
									}
									if util.S(rich["type"]) == "RICH_TEXT_NODE_TYPE_AT" {
										richNode.JumpURL = util.S(rich["jump_url"])
										richNode.Rid = util.S(rich["rid"])
										// 回退：如果 rid 为空，尝试 oid 字段
										if richNode.Rid == "" {
											richNode.Rid = util.S(rich["oid"])
										}
										// 回退：如果 rid 为空，从 jump_url 提取用户 ID
										if richNode.Rid == "" && richNode.JumpURL != "" {
											if m := regexp.MustCompile(`space\.bilibili\.com/(\d+)`).FindStringSubmatch(richNode.JumpURL); len(m) >= 2 {
												richNode.Rid = m[1]
											}
										}
										util.Log("DBG", "AT", "text=%q rid=%q jump_url=%q keys=%v", richNode.Text, richNode.Rid, richNode.JumpURL, func() []string {
											keys := make([]string, 0, len(rich))
											for k := range rich {
												keys = append(keys, k)
											}
											return keys
										}())
									}
									res.RichText = append(res.RichText, richNode)
								}
							}
						}
					}
					// Vote options
					linkCard := util.M(para["link_card"])
					if linkCard != nil {
						card := util.M(linkCard["card"])
						if card != nil {
							eva3Vote := util.M(card["eva3_vote"])
							if eva3Vote != nil {
								info := util.M(eva3Vote["info"])
								if info != nil {
									res.HasVote = true
									if res.VoteDesc == "" {
										res.VoteDesc = util.S(info["title"])
									}
									if jn, ok := info["join_num"].(float64); ok {
										res.VoteJoinNum = int(jn)
									}
									options := util.A(info["options"])
									for _, opt := range options {
										optMap := util.M(opt)
										if optMap == nil {
											continue
										}
										cnt := 0
										if c, ok := optMap["cnt"].(float64); ok {
											cnt = int(c)
										}
										pct := 0.0
										if res.VoteJoinNum > 0 {
											pct = float64(cnt) / float64(res.VoteJoinNum) * 100
										}
										res.VoteOptions = append(res.VoteOptions, BiliVoteOption{
											Desc:    util.S(optMap["opt_desc"]),
											Count:   cnt,
											Percent: pct,
										})
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

func parseBiliDynamicItem(item map[string]any) *BiliDynamicSimple {
	if item == nil {
		return nil
	}

	res := &BiliDynamicSimple{}

	dynamicID := util.S(item["id_str"])
	if dynamicID == "" {
		dynamicID = util.S(item["id"])
	}

	modules := util.M(item["modules"])
	if modules == nil {
		return res
	}

	// Author
	author := util.M(modules["module_author"])
	if author != nil {
		res.AuthorName = util.S(author["name"])
		res.AuthorAvatar = util.S(author["face"])
		res.PubTime = util.S(author["pub_time"])

		decorate := util.M(author["decorate"])
		if decorate != nil {
			fan := util.M(decorate["fan"])
			if fan != nil {
				res.HasFanCard = true
				res.FanCardName = util.S(decorate["name"])
				res.FanCardBg = util.S(decorate["card_url"])
				res.FanCardNum = util.S(fan["num_str"])
				res.FanCardColor = util.S(fan["color"])
			}
		}
	}

	// Dynamic content
	dyn := util.M(modules["module_dynamic"])
	if dyn != nil {
		topic := util.M(dyn["topic"])
		if topic != nil {
			res.Topic = util.S(topic["name"])
		}

		desc := util.M(dyn["desc"])
		if desc != nil {
			res.Text = util.S(desc["text"])
			nodes := util.A(desc["rich_text_nodes"])
			for _, n := range nodes {
				nodeMap := util.M(n)
				if nodeMap != nil {
					t := util.S(nodeMap["type"])
					text := util.S(nodeMap["text"])
					icon := ""
					size := 1
					if t == "RICH_TEXT_NODE_TYPE_EMOJI" {
						emoji := util.M(nodeMap["emoji"])
						if emoji != nil {
							icon = util.S(emoji["icon_url"])
							if s, ok := emoji["size"].(float64); ok {
								size = int(s)
							}
						}
					}
					jumpURL := ""
					if t == "RICH_TEXT_NODE_TYPE_WEB" {
						jumpURL = util.S(nodeMap["jump_url"])
					}
					rid := ""
					if t == "RICH_TEXT_NODE_TYPE_AT" {
						jumpURL = util.S(nodeMap["jump_url"])
						rid = util.S(nodeMap["rid"])
						// 回退：如果 rid 为空，尝试 oid 字段
						if rid == "" {
							rid = util.S(nodeMap["oid"])
						}
						// 回退：如果 rid 为空，从 jump_url 提取用户 ID
						if rid == "" && jumpURL != "" {
							if m := regexp.MustCompile(`space\.bilibili\.com/(\d+)`).FindStringSubmatch(jumpURL); len(m) >= 2 {
								rid = m[1]
							}
						}
					}
					res.RichText = append(res.RichText, BiliRichTextNode{
						Type:      t,
						Text:      text,
						JumpURL:   jumpURL,
						Rid:       rid,
						IconURL:   icon,
						EmojiSize: size,
					})
				}
			}
		}

		// Images
		major := util.M(dyn["major"])
		if major != nil {
			draw := util.M(major["draw"])
			if draw != nil {
				items := util.A(draw["items"])
				for _, i := range items {
					iMap := util.M(i)
					if iMap != nil {
						res.Images = append(res.Images, util.S(iMap["src"]))
					}
				}
			}
		}

		// Vote
		additional := util.M(dyn["additional"])
		if additional != nil && util.S(additional["type"]) == "ADDITIONAL_TYPE_VOTE" {
			vote := util.M(additional["vote"])
			if vote != nil {
				res.HasVote = true
				res.VoteDesc = util.S(vote["desc"])
				if jn, ok := vote["join_num"].(float64); ok {
					res.VoteJoinNum = int(jn)
				}
			}
		}
	}

	// Forward
	res.IsForward = false
	if item["orig"] != nil {
		res.IsForward = true
		orig := util.M(item["orig"])
		if orig != nil {
			res.Forward = parseBiliDynamicItem(orig)
		}
	}

	// Trigger fallback if this item is an Opus with missing text
	if res.Text == "" && res.Title == "" && dynamicID != "" {
		fetchAndParseOpus(dynamicID, res)
	}

	return res
}

// fetchOpusTitleAndDesc fetches the opus page HTML and extracts title + text content
// descHasContent checks whether a module_dynamic.desc map contains meaningful text content.
func descHasContent(v any) bool {
	d, ok := v.(map[string]any)
	if !ok || d == nil {
		return false
	}
	if t := util.S(d["text"]); t != "" {
		return true
	}
	if nodes := util.A(d["rich_text_nodes"]); len(nodes) > 0 {
		return true
	}
	return false
}

// extractInitialStateJSON extracts the JSON object assigned to window.__INITIAL_STATE__
// from HTML source. Uses brace counting instead of regex to correctly handle nested JSON.
func extractInitialStateJSON(html string) string {
	marker := "window.__INITIAL_STATE__"
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}
	rest := html[idx+len(marker):]
	// Skip optional whitespace and '='
	rest = strings.TrimLeft(rest, " \t\r\n")
	if !strings.HasPrefix(rest, "=") {
		return ""
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if !strings.HasPrefix(rest, "{") {
		return ""
	}
	depth := 0
	inStr := false
	escape := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return rest[:i+1]
			}
		}
	}
	return ""
}

// from __INITIAL_STATE__. Used as fallback when dynamic API returns desc=null for opus dynamics.
func fetchOpusTitleAndDesc(id string) (title string, desc map[string]any) {
	req, err := http.NewRequest("GET", "https://www.bilibili.com/opus/"+id, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	jsonStr := extractInitialStateJSON(string(body))
	if jsonStr == "" {
		return
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return
	}
	detail := util.M(data["detail"])
	if detail == nil {
		return
	}
	modules := util.A(detail["modules"])
	for _, m := range modules {
		mod := util.M(m)
		if mod == nil {
			continue
		}
		moduleType := util.S(mod["module_type"])
		if moduleType == "MODULE_TYPE_TITLE" {
			titleMap := util.M(mod["module_title"])
			if titleMap != nil {
				title = util.S(titleMap["text"])
			}
		} else if moduleType == "MODULE_TYPE_CONTENT" {
			contentMap := util.M(mod["module_content"])
			if contentMap == nil {
				continue
			}
			paragraphs := util.A(contentMap["paragraphs"])
			var richTextNodes []map[string]any
			for _, p := range paragraphs {
				para := util.M(p)
				if para == nil {
					continue
				}
				textMap := util.M(para["text"])
				if textMap == nil {
					continue
				}
				nodes := util.A(textMap["nodes"])
				for ni, n := range nodes {
					nMap := util.M(n)
					if nMap == nil {
						continue
					}
					nodeType := util.S(nMap["type"])
					if nodeType == "TEXT_NODE_TYPE_WORD" {
						word := util.M(nMap["word"])
						if word != nil {
							text := util.S(word["words"])
							// 如果下一个节点是 WEB，去掉文本末尾的空白/换行（保留开头换行用于正常换行）
							nextIsWEB := false
							if ni+1 < len(nodes) {
								if nextN := util.M(nodes[ni+1]); nextN != nil {
									if util.S(nextN["type"]) == "TEXT_NODE_TYPE_RICH" {
										if nextRich := util.M(nextN["rich"]); nextRich != nil {
											if util.S(nextRich["type"]) == "RICH_TEXT_NODE_TYPE_WEB" {
												nextIsWEB = true
											}
										}
									}
								}
							}
							if nextIsWEB {
								text = strings.TrimRight(text, " \n\r\t")
							}
							richTextNodes = append(richTextNodes, map[string]any{
								"type": "RICH_TEXT_NODE_TYPE_TEXT",
								"text": text,
							})
						}
					} else if nodeType == "TEXT_NODE_TYPE_RICH" {
						rich := util.M(nMap["rich"])
						if rich != nil {
							nodeMap := map[string]any{
								"type":     util.S(rich["type"]),
								"text":     util.S(rich["text"]),
								"jump_url": util.S(rich["jump_url"]),
							}
							if util.S(rich["type"]) == "RICH_TEXT_NODE_TYPE_AT" {
								util.Log("DBG", "AT-OPUS", "raw rich keys=%v rid=%q oid=%q jump_url=%q text=%q",
									func() []string {
										keys := make([]string, 0, len(rich))
										for k := range rich {
											keys = append(keys, k)
										}
										return keys
									}(),
									util.S(rich["rid"]), util.S(rich["oid"]), util.S(rich["jump_url"]), util.S(rich["text"]))
								rid := util.S(rich["rid"])
								// 回退：如果 rid 为空，尝试 oid 字段
								if rid == "" {
									rid = util.S(rich["oid"])
								}
								// 回退：如果 rid 为空，从 jump_url 提取用户 ID
								if rid == "" {
									if m := regexp.MustCompile(`space\.bilibili\.com/(\d+)`).FindStringSubmatch(util.S(rich["jump_url"])); len(m) >= 2 {
										rid = m[1]
									}
								}
								nodeMap["rid"] = rid
							}
							richTextNodes = append(richTextNodes, nodeMap)
						}
					}
				}
			}
			if len(richTextNodes) > 0 {
				desc = map[string]any{
					"rich_text_nodes": richTextNodes,
					"text":            "",
				}
			}
		}
	}
	return
}

func (r *Renderer) SaveBiliDynamic1(ctx context.Context, rawURL string, expand bool, atCard bool, linkQr bool) (string, error) {
	reId := regexp.MustCompile(`(?:t\.bilibili\.com/|opus/|dynamic/)(\d+)`)
	m := reId.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return "", fmt.Errorf("无法从链接提取动态ID: %s", rawURL)
	}
	id := m[1]

	util.Log("DBG", "Dynamic1", "SaveBiliDynamic1 called: id=%s expand=%v atCard=%v linkQr=%v", id, expand, atCard, linkQr)

	rawData, err := r.FetchBiliDynamicRaw(ctx, id)
	if err != nil {
		return "", err
	}

	// For opus dynamics, module_dynamic.desc may be null.
	// Fallback: fetch title and text from the opus page's __INITIAL_STATE__.
	if d, ok := rawData["data"].(map[string]any); ok {
		if item, ok := d["item"].(map[string]any); ok {
			if modules, ok := item["modules"].(map[string]any); ok {
				if md, ok := modules["module_dynamic"].(map[string]any); ok {
					if !descHasContent(md["desc"]) {
						util.Log("DBG", "Dynamic1", "desc为空，尝试从opus页面获取标题和文本")
						title, desc := fetchOpusTitleAndDesc(id)
						if title != "" || desc != nil {
							util.Log("DBG", "Dynamic1", "opus回退成功: title=%q, hasDesc=%v", title, desc != nil)
							if desc != nil {
								md["desc"] = desc
							}
							if title != "" {
								// Inject title into module_dynamic.major as article-like structure
								if major, ok := md["major"].(map[string]any); ok {
									if major["article"] == nil {
										major["article"] = map[string]any{"title": title}
									}
								} else {
									md["major"] = map[string]any{
										"type":    "MAJOR_TYPE_ARTICLE",
										"article": map[string]any{"title": title},
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// For forwarded dynamics, the orig item's desc may also be null (opus type).
	// Apply the same opus fallback to the original dynamic.
	if d, ok := rawData["data"].(map[string]any); ok {
		if item, ok := d["item"].(map[string]any); ok {
			if orig, ok := item["orig"].(map[string]any); ok {
				origID := util.S(orig["id_str"])
				if origID == "" {
					origID = util.S(orig["id"])
				}
				if origModules, ok := orig["modules"].(map[string]any); ok {
					if origMd, ok := origModules["module_dynamic"].(map[string]any); ok {
						if !descHasContent(origMd["desc"]) && origID != "" {
							util.Log("DBG", "Dynamic1", "转发原动态desc为空，尝试从opus页面获取 (orig_id=%s)", origID)
							origTitle, origDesc := fetchOpusTitleAndDesc(origID)
							if origTitle != "" || origDesc != nil {
								util.Log("DBG", "Dynamic1", "转发原动态opus回退成功: title=%q, hasDesc=%v", origTitle, origDesc != nil)
								if origDesc != nil {
									origMd["desc"] = origDesc
								}
								if origTitle != "" {
									if major, ok := origMd["major"].(map[string]any); ok {
										if major["article"] == nil {
											major["article"] = map[string]any{"title": origTitle}
										}
									} else {
										origMd["major"] = map[string]any{
											"type":    "MAJOR_TYPE_ARTICLE",
											"article": map[string]any{"title": origTitle},
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Collect AT mids from rich_text_nodes (after opus fallback, desc may have been set)
	atMids := map[string]bool{}
	if d, ok := rawData["data"].(map[string]any); ok {
		if item, ok := d["item"].(map[string]any); ok {
			if modules, ok := item["modules"].(map[string]any); ok {
				if md, ok := modules["module_dynamic"].(map[string]any); ok {
					if desc, ok := md["desc"].(map[string]any); ok {
						rawNodes := desc["rich_text_nodes"]
						var nodes []any
						switch v := rawNodes.(type) {
						case []any:
							nodes = v
						case []map[string]any:
							nodes = make([]any, len(v))
							for i, n := range v {
								nodes[i] = n
							}
						}
						for _, n := range nodes {
							if nm, ok := n.(map[string]any); ok {
								if util.S(nm["type"]) == "RICH_TEXT_NODE_TYPE_AT" {
									if rid := util.S(nm["rid"]); rid != "" {
										atMids[rid] = true
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Also collect forward-box author mid for AT card
	if d, ok := rawData["data"].(map[string]any); ok {
		if item, ok := d["item"].(map[string]any); ok {
			if orig, ok := item["orig"].(map[string]any); ok {
				if modules, ok := orig["modules"].(map[string]any); ok {
					if author, ok := modules["module_author"].(map[string]any); ok {
						if mid := util.S(author["mid"]); mid != "" {
							atMids[mid] = true
						}
					}
					// Collect AT mids from orig dynamic's rich_text_nodes (may have been set by opus fallback)
					if md, ok := modules["module_dynamic"].(map[string]any); ok {
						if desc, ok := md["desc"].(map[string]any); ok {
							rawNodes := desc["rich_text_nodes"]
							var nodes []any
							switch v := rawNodes.(type) {
							case []any:
								nodes = v
							case []map[string]any:
								nodes = make([]any, len(v))
								for i, n := range v {
									nodes[i] = n
								}
							}
							for _, n := range nodes {
								if nm, ok := n.(map[string]any); ok {
									if util.S(nm["type"]) == "RICH_TEXT_NODE_TYPE_AT" {
										if rid := util.S(nm["rid"]); rid != "" {
											atMids[rid] = true
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Pre-fetch AT user info from Go side (with cookies) to avoid browser CORS/rate-limit issues
	var atUserDataJSON string
	if atCard && len(atMids) > 0 {
		atUserInfoMap := make(map[string]any, len(atMids))
		for mid := range atMids {
			if info := r.fetchBiliUserInfo(ctx, mid); info != nil {
				atUserInfoMap[mid] = info
			}
		}
		if len(atUserInfoMap) > 0 {
			if jb, err := json.Marshal(atUserInfoMap); err == nil {
				atUserDataJSON = string(jb)
			}
		}
	}

	// Fetch vote options from VC API if dynamic contains a vote.
	// The dynamic detail API only returns vote metadata (desc, join_num, vote_id),
	// not the actual options. We need to fetch them separately.
	if d, ok := rawData["data"].(map[string]any); ok {
		if item, ok := d["item"].(map[string]any); ok {
			if modules, ok := item["modules"].(map[string]any); ok {
				if md, ok := modules["module_dynamic"].(map[string]any); ok {
					if additional, ok := md["additional"].(map[string]any); ok {
						if vote, ok := additional["vote"].(map[string]any); ok {
							if vid, ok := vote["vote_id"].(float64); ok && vid > 0 {
								voteInfo := r.fetchBiliVoteInfo(ctx, int64(vid))
								if voteInfo != nil {
									if options, ok := voteInfo["options"].([]any); ok && len(options) > 0 {
										vote["options"] = options
									}
								}
							}
						}
					}
				}
			}
		}
	}

	data := DynamicTemplateData{
		Platform:    "bili",
		RawURL:      rawURL,
		ImageBase64: "",
		RawData:     rawData,
		Timestamp:   time.Now().Format("15:04:05"),
		GeneratedAt: time.Now().Format("2006/01/02 15:04:05"),
		Expand:      expand,
		AtCard:      atCard,
		LinkQr:      linkQr,
	}

	tHTML, err := r.templateManager.RenderDynamicVariant("bili", 1, data)
	if err != nil {
		return "", fmt.Errorf("渲染 dynamic1 模板失败 (请检查 template 目录是否存在 bilidynamic1.tmpl): %v", err)
	}

	var injects []string
	if atCard || linkQr {
		injects = append(injects, biliDynamicQrCodeJs)
	}
	// Inject pre-fetched AT user data so the JS doesn't need to make API calls
	if atUserDataJSON != "" {
		b64 := base64.StdEncoding.EncodeToString([]byte(atUserDataJSON))
		// atob() treats bytes as Latin-1, not UTF-8. Use decodeURIComponent(escape()) to restore UTF-8.
		injects = append(injects, `window.__AT_USER_DATA__ = JSON.parse(decodeURIComponent(escape(atob('`+b64+`'))));`)
	}
	if atCard {
		injects = append(injects, biliDynamicAtCardJs)
	}
	if linkQr {
		injects = append(injects, biliDynamicLinkQrJs)
	}

	png, err := r.captureLiveHTML(ctx, tHTML, 0, "", 1, injects...)
	if err != nil {
		return "", err
	}

	_, u, err := r.store.SavePNG("bili_dynamic_tmpl", png)
	return u, err
}
