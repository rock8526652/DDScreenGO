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
	browser *browser.Browser
	store   *storage.Store
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
	return &Renderer{browser: br, store: store}
}

func (r *Renderer) LiveCard(ctx context.Context, info platform.LiveInfo, opt CardOptions) ([]byte, error) {
	html, selector, variant := r.liveCardHTML(ctx, info, opt)
	png, err := r.captureLiveHTML(ctx, html, opt.View, selector, variant)
	if err != nil {
		return nil, err
	}
	return postProcessLivePNG(png, opt.View, variant)
}

func (r *Renderer) SaveLiveCard(ctx context.Context, prefix string, info platform.LiveInfo, opt CardOptions) (string, error) {
	png, err := r.LiveCard(ctx, info, opt)
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

func (r *Renderer) SaveURLScreenshot(ctx context.Context, prefix, rawURL, selector string, view int) (string, error) {
	png, err := r.URLScreenshot(ctx, rawURL, selector, view)
	if err != nil {
		return "", err
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) SaveBiliDynamic(ctx context.Context, rawURL string, column bool, expand bool, prefix string) (string, error) {
	png, err := r.BiliDynamic(ctx, rawURL, column, expand)
	if err != nil {
		return "", err
	}
	_, u, err := r.store.SavePNG(prefix, png)
	return u, err
}

func (r *Renderer) BiliDynamic(ctx context.Context, rawURL string, column bool, expand bool) ([]byte, error) {
	id := biliDynamicID(rawURL)
	util.Log("DBG", "Render", "开始解析B站动态 | 原始URL: %s | 解析出ID: %s", rawURL, id)
	if id == "" {
		return nil, fmt.Errorf("动态地址不合法")
	}

	var lastErr error
	for _, target := range r.biliDynamicTargets(ctx, rawURL, id, column) {
		util.Log("DBG", "Render", "尝试B站动态目标提取方案: %s (选择器: %s)", target.URL, target.Selector)
		png, err := r.captureBiliDynamic(ctx, target, expand)
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

func (r *Renderer) captureBiliDynamic(ctx context.Context, target biliDynamicTarget, expand bool) ([]byte, error) {
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
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
	})
	_, _ = s.Do(ctx, "Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{
		"Referer":         target.URL,
		"Accept-Language": "zh-CN,zh;q=0.9",
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
		if (t.includes('出错啦') || t.includes('404') || t.includes('412')) return true;
		const txt = document.body ? document.body.innerText : '';
		if (txt.includes('安全风控') || txt.includes('请求被拒绝') || txt.includes('412')) return true;
		
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
		if (t.includes('出错啦') || t.includes('404') || t.includes('412')) return t;
		const txt = document.body ? document.body.innerText : '';
		if (txt.includes('安全风控') || txt.includes('请求被拒绝') || txt.includes('412')) return '412风控拦截';
		return '';
	})()`)
	
	var errReason string
	if len(resRaw) > 0 {
		_ = json.Unmarshal(resRaw, &errReason)
	}
	if errReason != "" {
		return nil, fmt.Errorf("检测到错误页面，快速失败拦截 (原因: %s)", errReason)
	}

	if expand {
		util.Log("DBG", "Render", "执行图片强制展开...")
		expandResult, expandErr := s.Eval(ctx, biliDynamicExpandJS)
		if expandErr != nil {
			util.Log("WRN", "Render", "展开JS执行失败: %v", expandErr)
		} else {
			util.Log("DBG", "Render", "展开JS返回: %s", string(expandResult))
		}
	}
	
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
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
		_ = s.SetViewport(ctx, 1180, 1400, 2, false)
	}
	if err := s.SetContent(ctx, html); err != nil {
		return nil, err
	}
	_ = s.WaitExpr(ctx, `document.fonts ? document.fonts.ready.then(() => true) : true`, 5*time.Second)
	_ = s.WaitExpr(ctx, `Array.from(document.images || []).every(img => img.complete)`, 8*time.Second)
	return s.Screenshot(ctx, selector, selector == "")
}

func (r *Renderer) captureLiveHTML(ctx context.Context, html string, view int, selector string, variant int) ([]byte, error) {
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
	_ = s.WaitExpr(ctx, `document.fonts ? document.fonts.ready.then(() => true) : true`, 6*time.Second)
	_ = s.WaitExpr(ctx, liveCardReadyJS, 18*time.Second)
	return s.Screenshot(ctx, selector, selector == "")
}

func (r *Renderer) liveCardHTML(ctx context.Context, info platform.LiveInfo, opt CardOptions) (string, string, int) {
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
			durationLabel = "下播时间："
			if ts, err := strconv.ParseInt(opt.Timestamp, 10, 64); err == nil {
				if ts > 9999999999 {
					ts = ts / 1000
				}
				duration = time.Unix(ts, 0).Format("2006-01-02 15:04")
			} else {
				duration = opt.Timestamp
			}
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")
	req.Header.Set("Referer", rawURL)

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
		body.WriteString(fmt.Sprintf(`<section class="platform"><h2>%s <span>%d</span></h2>`, util.Escape(p), total))
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			body.WriteString(fmt.Sprintf(`<div class="letter">%s</div>`, util.Escape(k)))
			for _, u := range groups[k] {
				pic := ""
				if u.Pic != "" {
					pic = fmt.Sprintf(`<img src="%s" referrerpolicy="no-referrer">`, util.Escape(u.Pic))
				}
				nameClass := "name"
				if textChar {
					nameClass += " ellipsis"
				}
				body.WriteString(fmt.Sprintf(`<div class="user">%s<div class="%s">%s<small>%s</small></div><b>%s</b></div>`, pic, nameClass, util.Escape(u.Name), util.Escape(u.UID), util.Escape(u.WatchType)))
			}
		}
		body.WriteString(`</section>`)
	}
	bgCSS := ""
	if strings.HasPrefix(bg, "http://") || strings.HasPrefix(bg, "https://") || strings.HasPrefix(bg, "data:") {
		bgCSS = fmt.Sprintf(`background-image:linear-gradient(rgba(0,0,0,.45),rgba(0,0,0,.45)),url(%q);background-size:cover;background-position:center;color:#fff;`, bg)
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;font-family:"Microsoft YaHei",Arial,sans-serif;background:#fff}.wrap{padding:42px;%s}.title{text-align:center;font-size:42px;font-weight:700;margin-bottom:32px}.cols{display:flex;gap:46px;align-items:flex-start}.platform{width:650px;flex:0 0 650px}h2{font-size:72px;font-style:italic;color:#ffa500;margin:0 0 24px}h2 span{font-size:42px;color:#ff6347}.letter{text-align:right;font-size:46px;color:#28d466;margin:12px 18px 8px}.user{display:flex;align-items:center;gap:12px;margin:8px 0;font-size:22px}.user img{width:52px;height:52px;border-radius:50%%;object-fit:cover}.name{flex:1;min-width:0}.name small{display:block;color:rgba(128,128,128,.86);font-size:18px}.ellipsis{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.user b{font-size:22px;color:#68b7ff;white-space:nowrap}.footer{text-align:center;margin-top:24px;color:#9aa3b5;font-size:22px}
</style></head><body><main class="wrap"><div class="title">DDBOT 订阅列表</div><div class="cols">%s</div><div class="footer">%s</div></main></body></html>`, bgCSS, body.String(), time.Now().Format("2006-01-02 15:04:05"))
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

func (r *Renderer) SaveWeiboDynamic(ctx context.Context, rawURL string, prefix string) (string, error) {
	png, err := r.WeiboDynamic(ctx, rawURL)
	if err != nil {
		return "", err
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
		"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
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
