package platform

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"dd_screen_go/internal/storage"
	"dd_screen_go/internal/util"
)

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

type Service struct {
	client *http.Client
	store  *storage.Store
	mu     sync.Mutex
	cache  map[string]cacheEntry
}

type LiveInfo struct {
	Platform    string
	RoomID      string
	Title       string
	Nickname    string
	Author      string
	Avatar      string
	Cover       string
	IsLiving    bool
	LiveURL     string
	SourceURL   string
	Category    string
	Description string
	UserCount   string
	StartTime   string
	StatusText  string

	// New fields for Bili Live3 cute card
	FollowerNum int
	MedalName   string
	GuardNum    int
}

type cacheEntry struct {
	expire time.Time
	data   any
}

func NewService(store *storage.Store) *Service {
	return &Service{
		store: store,
		client: &http.Client{
			Timeout: 25 * time.Second,
		},
		cache: map[string]cacheEntry{},
	}
}

func (s *Service) BiliLive(ctx context.Context, input string) (LiveInfo, error) {
	roomID := firstDigits(input)
	if roomID == "" {
		return LiveInfo{}, fmt.Errorf("房间号不能为空")
	}
	headers := biliHeaders("https://www.bilibili.com")
	if ck := s.store.CookieHeader("bili"); ck != "" {
		headers["Cookie"] = ck
	}
	var root map[string]any
	if err := s.getJSON(ctx, "https://api.live.bilibili.com/room/v1/Room/get_info?room_id="+url.QueryEscape(roomID), headers, &root); err != nil {
		return LiveInfo{}, err
	}
	data := util.M(root["data"])
	if data == nil {
		return LiveInfo{}, fmt.Errorf("Bilibili 房间数据为空")
	}
	uid := util.S(data["uid"])
	parentArea := strings.TrimSpace(util.S(data["parent_area_name"]))
	areaName := strings.TrimSpace(util.S(data["area_name"]))
	category := ""
	if parentArea != "" && areaName != "" {
		category = parentArea + "-" + areaName
	} else {
		category = firstNonEmpty(parentArea, areaName)
	}

	info := LiveInfo{
		Platform:    "Bilibili",
		RoomID:      firstNonEmpty(util.S(data["room_id"]), roomID),
		Title:       util.S(data["title"]),
		Cover:       firstNonEmpty(util.S(data["user_cover"]), util.S(data["cover"])),
		IsLiving:    util.S(data["live_status"]) == "1",
		LiveURL:     "https://live.bilibili.com/" + firstNonEmpty(util.S(data["room_id"]), roomID),
		Category:    category,
		Description: util.S(data["description"]),
		StartTime:   util.S(data["live_time"]),
	}
	if uid != "" {
		var userRoot map[string]any
		userURL := "https://api.live.bilibili.com/live_user/v1/Master/info?uid=" + url.QueryEscape(uid)
		if err := s.getJSON(ctx, userURL, biliHeaders(info.LiveURL), &userRoot); err == nil {
			ud := util.M(userRoot["data"])
			userInfo := util.M(ud["info"])
			info.Nickname = util.S(userInfo["uname"])
			info.Avatar = util.S(userInfo["face"])
			if news := util.M(ud["room_news"]); info.Description == "" && news != nil {
				info.Description = util.S(news["content"])
			}
			if f, ok := ud["follower_num"].(float64); ok {
				info.FollowerNum = int(f)
			}
			if g, ok := ud["guard_num"].(float64); ok {
				info.GuardNum = int(g)
			}
			// 优先使用更精准的 guardTab/topListNew API 获取大航海实时总数 (num)
			var guardRoot map[string]any
			guardURL := fmt.Sprintf("https://api.live.bilibili.com/xlive/app-room/v2/guardTab/topListNew?roomid=%s&page=1&ruid=%s", url.QueryEscape(info.RoomID), url.QueryEscape(uid))
			if err := s.getJSON(ctx, guardURL, biliHeaders(info.LiveURL), &guardRoot); err == nil {
				if gd := util.M(guardRoot["data"]); gd != nil {
					if ginfo := util.M(gd["info"]); ginfo != nil {
						if num, ok := ginfo["num"].(float64); ok {
							info.GuardNum = int(num)
						}
					}
				}
			}
			if name := util.S(ud["medal_name"]); name != "" {
				info.MedalName = name
			} else if medal := util.M(ud["medal"]); medal != nil {
				info.MedalName = util.S(medal["medal_name"])
			}
		}
	}
	return info, nil
}

func (s *Service) DouyuLive(ctx context.Context, input string) (LiveInfo, error) {
	roomID := parsePathID(input, `douyu\.com(?:/beta)?/([^/?#]+)`)
	if roomID == "" {
		roomID = strings.TrimSpace(input)
	}
	var root map[string]any
	err := s.getJSON(ctx, "https://www.douyu.com/betard/"+url.PathEscape(roomID), map[string]string{
		"User-Agent": desktopUA,
		"Referer":    "https://www.douyu.com/",
	}, &root)
	if err != nil {
		return LiveInfo{}, err
	}
	room := util.M(root["room"])
	if room == nil {
		return LiveInfo{}, fmt.Errorf("斗鱼房间数据为空")
	}
	avatar := util.S(util.Get(room, "avatar", "big"))
	cover := firstNonEmpty(util.S(room["rs1"]), util.S(room["room_src"]), avatar)
	if cover != "" && !strings.HasPrefix(cover, "http") {
		cover = "https://rpic.douyucdn.cn/" + strings.TrimPrefix(cover, "/")
	}
	showStatus := util.S(room["show_status"])
	videoLoop := util.S(room["videoLoop"])
	return LiveInfo{
		Platform:  "Douyu",
		RoomID:    roomID,
		Title:     util.S(room["room_name"]),
		Nickname:  util.S(room["nickname"]),
		Avatar:    avatar,
		Cover:     cover,
		IsLiving:  showStatus == "1" && videoLoop != "1",
		LiveURL:   firstNonEmpty(util.S(room["room_url"]), "https://www.douyu.com/"+roomID),
		Category:  util.S(room["cate_name"]),
		UserCount: util.S(room["online"]),
	}, nil
}

func (s *Service) HuyaLive(ctx context.Context, input string) (LiveInfo, error) {
	roomID := parsePathID(input, `huya\.com/([^/?#]+)`)
	if roomID == "" {
		roomID = strings.TrimSpace(input)
	}
	pageURL := "https://www.huya.com/" + roomID
	text, err := s.getText(ctx, pageURL, map[string]string{"User-Agent": desktopUA, "Referer": "https://www.huya.com/"})
	if err != nil {
		return LiveInfo{}, err
	}
	avatar := firstRe(text, `id="avatar-img"[^>]*src="([^"]+)"`)
	if strings.HasPrefix(avatar, "//") {
		avatar = "https:" + avatar
	}
	cover := firstRe(text, `"screenshot"\s*:\s*"([^"]+)"`)
	cover = strings.ReplaceAll(cover, `\/`, `/`)
	if strings.HasPrefix(cover, "//") {
		cover = "https:" + cover
	}
	return LiveInfo{
		Platform: "Huya",
		RoomID:   roomID,
		Title:    html.UnescapeString(firstRe(text, `class="host-title"[^>]*title="([^"]+)"`)),
		Nickname: html.UnescapeString(firstRe(text, `class="host-name"[^>]*title="([^"]+)"`)),
		Avatar:   avatar,
		Cover:    cover,
		IsLiving: strings.Contains(text, `class="host-spectator"`),
		LiveURL:  pageURL,
		Category: strings.TrimSpace(html.UnescapeString(firstRe(text, `class="host-channel"[^>]*>.*?<a[^>]*>([^<]+)</a>`))),
	}, nil
}

func (s *Service) ACFunLive(ctx context.Context, input string) (LiveInfo, error) {
	uid := firstDigits(input)
	if uid == "" {
		return LiveInfo{}, fmt.Errorf("AcFun UID 不能为空")
	}
	pageURL := "https://live.acfun.cn/live/" + uid
	text, err := s.getText(ctx, pageURL, map[string]string{"User-Agent": desktopUA, "Referer": "https://www.acfun.cn/"})
	if err != nil {
		return LiveInfo{}, err
	}
	jsonText := firstRe(text, `(?s)<script>window\.__INITIAL_STATE__=(.*?);\(`)
	var root map[string]any
	_ = json.Unmarshal([]byte(jsonText), &root)
	live := util.M(root["liveInfo"])
	user := util.M(live["user"])
	cover := ""
	if arr := util.A(live["coverUrls"]); len(arr) > 0 {
		cover = util.S(arr[0])
	}
	return LiveInfo{
		Platform:    "AcFun",
		RoomID:      uid,
		Title:       util.S(live["title"]),
		Nickname:    util.S(user["name"]),
		Avatar:      util.S(user["headUrl"]),
		Cover:       firstNonEmpty(cover, util.S(user["headUrl"])),
		IsLiving:    util.S(live["liveId"]) != "",
		LiveURL:     pageURL,
		Category:    firstNonEmpty(util.S(util.Get(live, "type", "name")), util.S(util.Get(live, "type", "categoryName"))),
		Description: util.S(user["signature"]),
		UserCount:   util.S(live["onlineCount"]),
	}, nil
}

func (s *Service) TwitchLive(ctx context.Context, input string) (LiveInfo, error) {
	login := parsePathID(input, `twitch\.tv/([^/?#]+)`)
	if login == "" {
		login = strings.Trim(strings.TrimSpace(input), "/")
	}
	pageURL := "https://www.twitch.tv/" + login
	text, err := s.getText(ctx, pageURL, map[string]string{"User-Agent": desktopUA, "Accept-Language": "zh-CN,zh;q=0.9"})
	if err != nil {
		return LiveInfo{}, err
	}
	name := strings.TrimSuffix(html.UnescapeString(metaContent(text, "og:title")), " - Twitch")
	desc := html.UnescapeString(metaContent(text, "og:description"))
	cover := firstNonEmpty(jsonLDString(text, "thumbnailUrl"), "https://static-cdn.jtvnw.net/previews-ttv/live_user_"+login+"-640x360.jpg")
	return LiveInfo{
		Platform: "Twitch",
		RoomID:   login,
		Title:    desc,
		Nickname: firstNonEmpty(name, login),
		Avatar:   metaContent(text, "og:image"),
		Cover:    cover,
		IsLiving: strings.Contains(text, `"isLiveBroadcast":true`),
		LiveURL:  pageURL,
	}, nil
}

func (s *Service) NicoLive(ctx context.Context, input string) (LiveInfo, error) {
	liveID := parsePathID(input, `live\.nicovideo\.jp/watch/([^/?#]+)`)
	if liveID == "" {
		liveID = strings.TrimSpace(input)
	}
	pageURL := "https://live.nicovideo.jp/watch/" + liveID
	text, err := s.getText(ctx, pageURL, map[string]string{"User-Agent": desktopUA, "Accept-Language": "ja,zh-CN;q=0.9"})
	if err != nil {
		return LiveInfo{}, err
	}
	var embedded map[string]any
	props := html.UnescapeString(firstRe(text, `(?s)<script\s+id="embedded-data"\s+data-props="([^"]+)"`))
	_ = json.Unmarshal([]byte(props), &embedded)
	program := util.M(embedded["program"])
	supplier := util.M(program["supplier"])
	icons := util.M(supplier["icons"])
	thumb := util.M(program["thumbnail"])
	huge := util.M(thumb["huge"])
	return LiveInfo{
		Platform:  "Nico",
		RoomID:    liveID,
		Title:     firstNonEmpty(util.S(program["title"]), metaContent(text, "og:title")),
		Nickname:  util.S(supplier["name"]),
		Avatar:    firstNonEmpty(util.S(icons["uri150x150"]), util.S(icons["uri50x50"])),
		Cover:     firstNonEmpty(util.S(huge["s1280x720"]), util.S(huge["s640x360"]), metaContent(text, "og:image")),
		IsLiving:  util.S(program["status"]) == "ON_AIR" || strings.Contains(text, `"isLiveBroadcast":true`),
		LiveURL:   pageURL,
		Category:  "",
		UserCount: util.S(util.Get(program, "statistics", "watchCount")),
	}, nil
}

func (s *Service) YoutubeCard(ctx context.Context, input string) (LiveInfo, error) {
	videoID := youtubeID(input)
	if videoID == "" {
		return LiveInfo{}, fmt.Errorf("视频URL不合法")
	}
	videoURL := "https://www.youtube.com/watch?v=" + videoID
	var root map[string]any
	oembed := "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(videoURL)
	if err := s.getJSON(ctx, oembed, map[string]string{"User-Agent": desktopUA}, &root); err != nil {
		return LiveInfo{}, err
	}
	return LiveInfo{
		Platform:   "YouTube",
		RoomID:     videoID,
		Title:      util.S(root["title"]),
		Nickname:   util.S(root["author_name"]),
		Author:     util.S(root["author_name"]),
		Avatar:     util.S(root["thumbnail_url"]),
		Cover:      firstNonEmpty("https://i.ytimg.com/vi/"+videoID+"/maxresdefault.jpg", util.S(root["thumbnail_url"])),
		LiveURL:    videoURL,
		SourceURL:  videoURL,
		StatusText: "视频",
	}, nil
}

func (s *Service) DouyinLive(ctx context.Context, input string) (LiveInfo, error) {
	roomID := parsePathID(input, `live\.douyin\.com/([^/?#]+)`)
	if roomID == "" {
		roomID = strings.TrimSpace(input)
	}
	pageURL := roomID
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = "https://live.douyin.com/" + roomID
	}
	
	headers := map[string]string{
		"User-Agent": desktopUA,
		"Referer":    "https://www.douyin.com/",
	}
	if ck := s.store.CookieHeader("douyin"); ck != "" {
		headers["Cookie"] = ck
	}
	
	text, err := s.getText(ctx, pageURL, headers)
	if err != nil {
		return LiveInfo{
			Platform: "Douyin", RoomID: roomID, Title: "抖音直播间", LiveURL: pageURL, SourceURL: pageURL, IsLiving: true,
		}, nil
	}

	// 1. Title
	var title string
	// Try JSON format title
	if m := regexp.MustCompile(`\\?"title\\?"\s*:\s*\\?"([^"]+?)\\?"\s*,\s*\\?"user_count_str\\?"`).FindStringSubmatch(text); len(m) > 1 {
		title = m[1]
	}
	// Try HTML-encoded format title
	if title == "" {
		if m := regexp.MustCompile(`&quot;is_game_live&quot;\s*:\s*\d+\s*,\s*&quot;title&quot;\s*:\s*&quot;([^&]+?)&quot;`).FindStringSubmatch(text); len(m) > 1 {
			title = m[1]
		}
	}
	// Fallback to og:title meta content
	if title == "" {
		title = metaContent(text, "og:title")
	}
	if title == "" {
		title = "抖音直播间"
	}

	// 2. Nickname
	var nickname string
	// Try HTML-encoded format first
	if m := regexp.MustCompile(`&quot;nickname&quot;\s*:\s*&quot;([^&]+?)&quot;\s*,\s*&quot;avatar&quot;`).FindStringSubmatch(text); len(m) > 1 {
		nickname = m[1]
	}
	// Try JSON format
	if nickname == "" {
		if m := regexp.MustCompile(`\\?"nickname\\?"\s*:\s*\\?"([^"\\]+?)\\?"\s*,\s*\\?"avatar_thumb\\?"`).FindStringSubmatch(text); len(m) > 1 {
			if m[1] != "$undefined" {
				nickname = m[1]
			}
		}
	}
	// Try extraction from title
	if nickname == "" && title != "抖音直播间" && strings.Contains(title, "的") {
		nickname = strings.TrimSpace(strings.Split(title, "的")[0])
	}
	if nickname == "" {
		nickname = "抖音主播"
	}

	// 3. Avatar
	var avatar string
	// Try HTML-encoded format first
	if m := regexp.MustCompile(`&quot;nickname&quot;\s*:\s*&quot;[^&]+?&quot;\s*,\s*&quot;avatar&quot;\s*:\s*&quot;([^&]+?)&quot;`).FindStringSubmatch(text); len(m) > 1 {
		avatar = m[1]
	}
	// Try JSON format
	if avatar == "" {
		if m := regexp.MustCompile(`\\?"avatar_thumb\\?"\s*:\s*\{[^}]*\\?"url_list\\?"\s*:\s*\[\s*\\?"([^"]+?)\\?"`).FindStringSubmatch(text); len(m) > 1 {
			avatar = m[1]
		}
	}
	// Clean up avatar URL
	if avatar != "" {
		avatar = strings.ReplaceAll(avatar, `\/`, `/`)
		avatar = strings.ReplaceAll(avatar, `\u0026`, `&`)
		avatar = strings.ReplaceAll(avatar, `&amp;`, `&`)
		avatar = strings.ReplaceAll(avatar, `&#x2F;`, `/`)
		avatar = strings.ReplaceAll(avatar, `&#x3D;`, `=`)
	}
	// Fallback to og:image meta content
	if avatar == "" {
		avatar = metaContent(text, "og:image")
	}

	// 4. Cover
	var cover string
	// Try JSON format cover
	if m := regexp.MustCompile(`\\?"cover\\?"\s*:\s*\{[^{}]*\\?"url_list\\?"\s*:\s*\[\s*\\?"([^"]+?)\\?"`).FindStringSubmatch(text); len(m) > 1 {
		cover = m[1]
	}
	// Try HTML-encoded format cover (poster)
	if cover == "" {
		if m := regexp.MustCompile(`&quot;poster&quot;\s*:\s*&quot;([^&]+?)&quot;`).FindStringSubmatch(text); len(m) > 1 {
			cover = m[1]
		}
	}
	// Clean up cover URL
	if cover != "" {
		cover = strings.ReplaceAll(cover, `\/`, `/`)
		cover = strings.ReplaceAll(cover, `\u0026`, `&`)
		cover = strings.ReplaceAll(cover, `&amp;`, `&`)
		cover = strings.ReplaceAll(cover, `&#x2F;`, `/`)
		cover = strings.ReplaceAll(cover, `&#x3D;`, `=`)
	}
	// Fallback to og:image meta content
	if cover == "" {
		cover = metaContent(text, "og:image")
	}

	// 5. IsLiving
	isLiving := strings.Contains(text, `"status":2`) || strings.Contains(text, `&quot;status&quot;:2`) || strings.Contains(text, `"isLiveBroadcast":true`)
	if !isLiving {
		// fallback to check if we found active stream URLs
		isLiving = strings.Contains(text, "flv_pull_url") || strings.Contains(text, "hls_pull_url")
	}

	// 6. UserCount
	var userCount string
	if m := regexp.MustCompile(`\\?"user_count_str\\?"\s*:\s*\\?"([^"\\]+?)\\?"`).FindStringSubmatch(text); len(m) > 1 {
		userCount = m[1]
	}

	util.Log("DBG", "Platform", "抖音直播间解析完毕 | 房间号: %s | 标题: %s | 主播: %s | 在线人数: %s | 状态: %v",
		roomID, title, nickname, userCount, isLiving)

	return LiveInfo{
		Platform:   "Douyin",
		RoomID:     roomID,
		Title:      title,
		Nickname:   nickname,
		Author:     nickname,
		Avatar:     avatar,
		Cover:      cover,
		IsLiving:   isLiving,
		LiveURL:    pageURL,
		SourceURL:  pageURL,
		UserCount:  userCount,
	}, nil
}

func (s *Service) WeiboMobileProfile(ctx context.Context, uid string) (string, error) {
	return s.weiboMobile(ctx, "100505"+uid, uid)
}

func (s *Service) WeiboMobileCards(ctx context.Context, uid string) (string, error) {
	return s.weiboMobile(ctx, "107603"+uid, uid)
}

func (s *Service) weiboMobile(ctx context.Context, containerID, uid string) (string, error) {
	headers := map[string]string{
		"User-Agent":       "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
		"Referer":          "https://m.weibo.cn/u/" + uid,
		"Accept":           "application/json, text/plain, */*",
		"MWeibo-Pwa":       "1",
		"X-Requested-With": "XMLHttpRequest",
		"Accept-Language":  "zh-CN,zh;q=0.9",
	}
	if ck := s.store.CookieHeader("weibo"); ck != "" {
		// Filter cookies for m.weibo.cn and weibo.cn to avoid sending weibo.com cookies
		cookies, _ := s.store.ReadCookies("weibo")
		var mobileParts []string
		for _, c := range cookies {
			if strings.Contains(c.Domain, "weibo.cn") || strings.Contains(c.Domain, "m.weibo.cn") {
				if c.Name != "" && c.Value != "" {
					mobileParts = append(mobileParts, c.Name+"="+c.Value)
				}
				if strings.EqualFold(c.Name, "XSRF-TOKEN") {
					headers["X-XSRF-TOKEN"] = c.Value
				}
			}
		}
		if len(mobileParts) > 0 {
			headers["Cookie"] = strings.Join(mobileParts, "; ")
		}
	}
	return s.getText(ctx, "https://m.weibo.cn/api/container/getIndex?containerid="+url.QueryEscape(containerID), headers)
}

func (s *Service) XHHProfileEvents(ctx context.Context, token, userid, deviceID, deviceInfo, ua string, smid bool) (string, error) {
	path := "/bbs/app/profile/events"
	now := time.Now().Unix()
	nonce := xhhNonce()
	params := url.Values{}
	params.Set("os_type", "web")
	params.Set("app", "heybox")
	params.Set("client_type", "web")
	params.Set("version", "999.0.4")
	params.Set("web_version", "2.5")
	params.Set("x_client_type", "web")
	params.Set("x_app", "heybox_website")
	params.Set("heybox_id", "")
	params.Set("x_os_type", "Windows")
	params.Set("device_info", firstNonEmpty(deviceInfo, "Chrome"))
	params.Set("device_id", deviceID)
	params.Set("hkey", xhhHkey(path, now-5, nonce))
	params.Set("_time", fmt.Sprint(now))
	params.Set("nonce", nonce)
	params.Set("list_type", "moment")
	params.Set("userid", userid)
	params.Set("dw", "409")
	params.Set("lastval", "")
	headers := map[string]string{
		"Origin":     "https://www.xiaoheihe.cn",
		"Referer":    "https://www.xiaoheihe.cn/",
		"User-Agent": firstNonEmpty(ua, desktopUA),
	}
	if smid {
		headers["Cookie"] = "smidV2=" + token
	} else {
		headers["Cookie"] = "x_xhh_tokenid=" + token
	}
	return s.getText(ctx, "https://api.xiaoheihe.cn"+path+"?"+params.Encode(), headers)
}

func (s *Service) XHHTreeEvents(ctx context.Context, token, linkID, deviceID, deviceInfo, ua string) (string, error) {
	path := "/bbs/app/link/tree"
	now := time.Now().Unix()
	nonce := xhhNonce()
	params := url.Values{}
	for k, v := range map[string]string{
		"os_type": "web", "app": "heybox", "client_type": "web", "version": "999.0.4", "web_version": "2.5",
		"x_client_type": "web", "x_app": "heybox_website", "heybox_id": "", "x_os_type": "Windows",
		"device_info": firstNonEmpty(deviceInfo, "Chrome"), "device_id": deviceID, "hkey": xhhHkey(path, now-5, nonce),
		"_time": fmt.Sprint(now), "nonce": nonce, "h_src": "", "link_id": linkID, "is_first": "1", "page": "1", "index": "1", "limit": "20", "owner_only": "0",
	} {
		params.Set(k, v)
	}
	return s.getText(ctx, "https://api.xiaoheihe.cn"+path+"?"+params.Encode(), map[string]string{
		"Origin": "https://www.xiaoheihe.cn", "Referer": "https://www.xiaoheihe.cn/",
		"User-Agent": firstNonEmpty(ua, desktopUA), "Cookie": "x_xhh_tokenid=" + token,
	})
}

func (s *Service) getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	text, err := s.getText(ctx, rawURL, headers)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), out)
}

func (s *Service) getText(ctx context.Context, rawURL string, headers map[string]string) (string, error) {
	util.Log("DBG", "Platform", "请求上游接口: GET %s", rawURL)
	if v, ok := s.fromCache(rawURL); ok {
		return v.(string), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if headers["User-Agent"] == "" {
		headers["User-Agent"] = desktopUA
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, rawURL)
	}
	text := string(body)
	s.setCache(rawURL, text, 30*time.Second)
	return text, nil
}

func (s *Service) PostJSON(ctx context.Context, rawURL string, headers map[string]string, body any) (string, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("User-Agent", desktopUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, rawURL)
	}
	return string(out), nil
}

func (s *Service) fromCache(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[key]
	if !ok || time.Now().After(entry.expire) {
		delete(s.cache, key)
		return nil, false
	}
	return entry.data, true
}

func (s *Service) setCache(key string, data any, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = cacheEntry{expire: time.Now().Add(ttl), data: data}
}

func biliHeaders(referer string) map[string]string {
	return map[string]string{
		"Origin":     referer,
		"Referer":    strings.TrimRight(referer, "/") + "/",
		"User-Agent": desktopUA,
	}
}

func firstDigits(s string) string {
	return firstRe(s, `(\d+)`)
}

func parsePathID(input, pattern string) string {
	re := regexp.MustCompile(`(?i)` + pattern)
	if m := re.FindStringSubmatch(strings.TrimSpace(input)); len(m) > 1 {
		return m[1]
	}
	return ""
}

func firstRe(s, pattern string) string {
	re := regexp.MustCompile(pattern)
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func metaContent(s, property string) string {
	p := regexp.QuoteMeta(property)
	return firstNonEmpty(
		firstRe(s, `(?is)<meta\s+(?:property|name)="`+p+`"\s+content="([^"]*)"`),
		firstRe(s, `(?is)<meta\s+content="([^"]*)"\s+(?:property|name)="`+p+`"`),
	)
}

func jsonLDString(s, key string) string {
	raw := firstRe(s, `(?s)<script\s+type="application/ld\+json">(.*?)</script>`)
	if raw == "" {
		return ""
	}
	var data any
	if json.Unmarshal([]byte(raw), &data) != nil {
		return ""
	}
	return findKeyString(data, key)
}

func findKeyString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[key]; ok {
			return util.FirstString(val)
		}
		for _, val := range x {
			if s := findKeyString(val, key); s != "" {
				return s
			}
		}
	case []any:
		for _, val := range x {
			if s := findKeyString(val, key); s != "" {
				return s
			}
		}
	}
	return ""
}

func youtubeID(input string) string {
	return firstNonEmpty(
		firstRe(input, `(?i)youtube\.com/watch\?v=([a-zA-Z0-9_-]{11})`),
		firstRe(input, `(?i)youtu\.be/([a-zA-Z0-9_-]{11})`),
		firstRe(input, `(?i)youtube\.com/shorts/([a-zA-Z0-9_-]{11})`),
	)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func RandomDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func GenerateSmidV2() string {
	ts := time.Now().Format("20060102150405")
	uidBytes := make([]byte, 16)
	_, _ = rand.Read(uidBytes)
	uid := hex.EncodeToString(uidBytes)
	part1 := ts + md5Hex(uid) + "00"
	return part1 + md5Hex("smsk_web_" + part1)[:14] + "0"
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func xhhNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

const xhhTable = "AB45STUVWZEFGJ6CH01D237IXYPQRKLMN89"

func xhhHkey(path string, t int64, nonce string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	combined := interleave([]string{
		av(fmt.Sprint(t), xhhTable, -2),
		sv(path, xhhTable),
		sv(nonce, xhhTable),
	})
	if len(combined) > 20 {
		combined = combined[:20]
	}
	hash := md5Hex(combined)
	first5 := hash[:5]
	last6 := hash[len(hash)-6:]
	codes := []int{int(last6[0]), int(last6[1]), int(last6[2]), int(last6[3]), int(last6[4]), int(last6[5])}
	km := xhhKM(codes[:4])
	sum := codes[4] + codes[5]
	for _, v := range km {
		sum += v
	}
	return av(first5, xhhTable, -4) + fmt.Sprintf("%02d", sum%100)
}

func av(e, table string, n int) string {
	limit := n
	if n < 0 {
		limit = len(table) + n
	}
	if limit <= 0 || limit > len(table) {
		limit = len(table)
	}
	t := table[:limit]
	var b strings.Builder
	for _, c := range e {
		b.WriteByte(t[int(c)%len(t)])
	}
	return b.String()
}

func sv(e, table string) string {
	var b strings.Builder
	for _, c := range e {
		b.WriteByte(table[int(c)%len(table)])
	}
	return b.String()
}

func interleave(parts []string) string {
	maxLen := 0
	for _, p := range parts {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	var b strings.Builder
	for i := 0; i < maxLen; i++ {
		for _, p := range parts {
			if i < len(p) {
				b.WriteByte(p[i])
			}
		}
	}
	return b.String()
}

func xhhVM(e int) int {
	if e&128 != 0 {
		return (e<<1 ^ 27) & 0xff
	}
	return (e << 1) & 0xff
}

func xhhQM(e int) int { return xhhVM(e) ^ e }
func xhhDM(e int) int { return xhhQM(xhhVM(e)) }
func xhhYM(e int) int { return xhhDM(xhhQM(xhhVM(e))) }
func xhhGM(e int) int { return xhhYM(e) ^ xhhDM(e) ^ xhhQM(e) }

func xhhKM(e []int) []int {
	return []int{
		xhhGM(e[0]) ^ xhhYM(e[1]) ^ xhhDM(e[2]) ^ xhhQM(e[3]),
		xhhQM(e[0]) ^ xhhGM(e[1]) ^ xhhYM(e[2]) ^ xhhDM(e[3]),
		xhhDM(e[0]) ^ xhhQM(e[1]) ^ xhhGM(e[2]) ^ xhhYM(e[3]),
		xhhYM(e[0]) ^ xhhDM(e[1]) ^ xhhQM(e[2]) ^ xhhGM(e[3]),
	}
}
