package render

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dd_screen_go/internal/util"
)

const xhhHtmlTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: 'Noto Sans SC', -apple-system, 'PingFang SC', 'Helvetica Neue', sans-serif;
    background: #F7F8F9;
    color: #14191E;
    -webkit-font-smoothing: antialiased;
    display: flex;
    justify-content: center;
    padding: 24px;
  }
  .post-card {
    width: 660px;
    background: #FFFFFF;
    border-radius: 12px;
    overflow: hidden;
    box-shadow: 0 2px 12px rgba(0,0,0,0.06);
  }
  .header-image {
    width: 100%;
    position: relative;
    background: #F3F4F5;
    display: flex;
    justify-content: center;
    overflow: hidden;
  }
  .header-image:empty { display: none; }
  .header-image img {
    display: block;
    max-height: 500px;
    width: auto;
    max-width: 100%;
    object-fit: contain;
  }
  .content { padding: 20px 24px 24px; }
  .author-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
  .avatar-wrap { position: relative; width: 42px; height: 42px; flex-shrink: 0; }
  .avatar-wrap .avatar { width: 42px; height: 42px; border-radius: 50%; object-fit: cover; }
  .avatar-wrap .decoration { position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%); width: 52px; height: 52px; pointer-events: none; }
  .author-info { flex: 1; min-width: 0; }
  .author-name-line { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .author-name { font-size: 15px; font-weight: 700; color: #14191E; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .medal-icon { height: 16px; width: auto; vertical-align: middle; flex-shrink: 0; }
  .level-tag { display: inline-flex; align-items: center; font-size: 10px; font-weight: 700; color: #FFF; background: linear-gradient(135deg, #464B50, #14191E); border-radius: 3px; padding: 1px 5px; white-space: nowrap; flex-shrink: 0; line-height: 16px; }
  .follow-btn { display: flex; align-items: center; gap: 2px; background: linear-gradient(46deg, #464B50, #14191E); color: #FFF; font-size: 12px; font-weight: 700; border: none; border-radius: 4px; padding: 4px 10px 4px 6px; flex-shrink: 0; line-height: 1; }
  .follow-btn svg { width: 14px; height: 14px; }
  .post-title { font-size: 20px; font-weight: 700; line-height: 1.4; color: #14191E; margin-bottom: 10px; }
  .post-desc { font-size: 15px; line-height: 1.7; color: #14191E; margin-bottom: 16px; word-break: break-all; }
  .post-desc p { margin: 8px 0; }
  .post-desc b { font-weight: 700; }
  .post-desc img { max-width: 100%; border-radius: 8px; margin: 8px 0; display: block; }
  .post-desc h2 { font-size: 17px; font-weight: 700; margin: 16px 0 8px; }
  .post-desc h3 { font-size: 16px; font-weight: 700; margin: 12px 0 6px; }
  .post-desc blockquote { margin: 6px 0; padding: 8px 12px; border-left: 3px solid #E0E2E4; background: #F8F9FA; border-radius: 4px; color: #64696E; font-size: 14px; }
  .tags-row { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 14px; }
  .tag { display: inline-flex; align-items: center; gap: 4px; padding: 5px 10px; border-radius: 6px; font-size: 12px; font-weight: 500; line-height: 1; white-space: nowrap; }
  .tag img { width: 16px; height: 16px; border-radius: 3px; object-fit: cover; }
  .meta-row { display: flex; align-items: center; gap: 16px; font-size: 13px; color: #8C9196; padding-top: 12px; border-top: 1px solid #F3F4F5; }
  .meta-row .dot { width: 3px; height: 3px; border-radius: 50%; background: #C8CDD2; flex-shrink: 0; }
  .stats-row { display: flex; align-items: center; gap: 24px; margin-top: 14px; padding-top: 14px; border-top: 1px solid #F3F4F5; }
  .stat-item { display: flex; align-items: center; gap: 5px; font-size: 13px; color: #64696E; }
  .stat-item svg { width: 18px; height: 18px; color: #8C9196; }
  .watermark { display: flex; align-items: center; justify-content: center; gap: 6px; padding: 10px; background: #FAFBFC; border-top: 1px solid #F3F4F5; font-size: 11px; color: #C8CDD2; }
  .watermark img { width: 14px; height: 14px; opacity: 0.4; }
</style>
</head>
<body>
<div class="post-card">
  <div class="header-image">{{HEADER_IMAGES}}</div>
  <div class="content">
    <div class="author-row">
      <div class="avatar-wrap">
        <img class="avatar" src="{{AVATAR_URL}}" alt="">
        {{AVATAR_DECORATION}}
      </div>
      <div class="author-info">
        <div class="author-name-line">
          <span class="author-name">{{USERNAME}}</span>
          {{MEDAL_ICONS}}
          <span class="level-tag">Lv.{{LEVEL}}</span>
        </div>
      </div>
      <button class="follow-btn">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
          <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
        </svg>
        关注
      </button>
    </div>
    <div class="post-title">{{TITLE}}</div>
    <div class="post-desc">{{DESCRIPTION}}</div>
    <div class="tags-row">{{CONTENT_TAGS}}</div>
    <div class="meta-row">
      <span>{{TIME_TEXT}}</span>
      <div class="dot"></div>
      <span>{{IP_LOCATION}}</span>
      <div class="dot"></div>
      <span>阅读 {{CLICK}}</span>
    </div>
    <div class="stats-row">
      <div class="stat-item"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3zM7 22H4a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h3"></path></svg> {{LINK_AWARD_NUM}}</div>
      <div class="stat-item"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"></path></svg> {{COMMENT_NUM}}</div>
      <div class="stat-item"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"></polygon></svg> {{FAVOUR_COUNT}}</div>
    </div>
  </div>
  <div class="watermark">
    <span>DD_SCREEN GO · 小黑盒动态</span>
  </div>
</div>
</body>
</html>`

func xhhReplaceEmoji(text string) string {
	re := regexp.MustCompile(`\[([^\]]+)\]`)
	return re.ReplaceAllStringFunc(text, func(m string) string {
		emojiName := m[1 : len(m)-1]
		return fmt.Sprintf(`<img style="width:20px;height:20px;vertical-align:middle;" src="https://cdn.max-c.com/heybox/bbs/emoji/cube/%s.png" onerror="this.style.display='none'" alt="%s">`, html.EscapeString(emojiName), html.EscapeString(emojiName))
	})
}

func xhhParseLightColor(colorStr, fallback string) string {
	if colorStr == "" {
		return fallback
	}
	parts := strings.Split(colorStr, "#")
	if len(parts) == 0 {
		return fallback
	}
	hex := parts[0]
	if len(parts) > 1 && parts[0] == "" {
		hex = parts[1]
	}
	if len(hex) == 8 {
		a, _ := strconv.ParseUint(hex[0:2], 16, 8)
		r, _ := strconv.ParseUint(hex[2:4], 16, 8)
		g, _ := strconv.ParseUint(hex[4:6], 16, 8)
		b, _ := strconv.ParseUint(hex[6:8], 16, 8)
		alpha := math.Round(float64(a)/255.0*100) / 100
		return fmt.Sprintf("rgba(%d,%d,%d,%v)", r, g, b, alpha)
	}
	if len(hex) == 6 {
		return "#" + hex
	}
	return fallback
}

func xhhFormatTime(timestamp int64) string {
	dt := time.Unix(timestamp, 0)
	diff := time.Since(dt)
	if diff.Minutes() < 1 {
		return "刚刚"
	}
	if diff.Minutes() < 60 {
		return fmt.Sprintf("%d分钟前", int(diff.Minutes()))
	}
	if diff.Hours() < 24 {
		return fmt.Sprintf("%d小时前", int(diff.Hours()))
	}
	if diff.Hours() < 24*30 {
		return fmt.Sprintf("%d天前", int(diff.Hours()/24))
	}
	return dt.Format("2006-01-02")
}

func buildXHHHtml(apiData map[string]any) (string, error) {
	link, ok := apiData["link"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid result: link field missing")
	}
	user, ok := link["user"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid result: user field missing")
	}

	isArticle := false
	if isArt, ok := link["is_article"].(float64); ok && isArt == 1 {
		isArticle = true
	}

	headerImageHtml := ""
	articleBodyHtml := ""

	textJsonRaw, _ := link["text"].(string)
	if textJsonRaw != "" {
		var textArr []map[string]any
		if err := json.Unmarshal([]byte(textJsonRaw), &textArr); err == nil {
			firstImg := true
			for _, item := range textArr {
				tType, _ := item["type"].(string)
				if tType == "html" {
					htmlContent, _ := item["text"].(string)
					// Replace data-original with src
					re1 := regexp.MustCompile(`(?i)<img\s+[^>]*?data-original="([^"]+)"[^>]*/?>`)
					htmlContent = re1.ReplaceAllStringFunc(htmlContent, func(m string) string {
						match := re1.FindStringSubmatch(m)
						if len(match) > 1 {
							return fmt.Sprintf(`<img src="%s" alt="">`, match[1])
						}
						return m
					})
					// Replace [cube_xxx]
					htmlContent = xhhReplaceEmoji(htmlContent)
					articleBodyHtml += htmlContent
				} else if tType == "img" {
					imgUrl, _ := item["url"].(string)
					if imgUrl != "" {
						if !isArticle && firstImg {
							headerImageHtml += fmt.Sprintf(`<img src="%s" alt="">`, html.EscapeString(imgUrl))
							firstImg = false
						}
					}
				} else if tType == "text" {
					text, _ := item["text"].(string)
					text = xhhReplaceEmoji(html.EscapeString(text))
					text = strings.ReplaceAll(text, "\n", "<br>")
					articleBodyHtml += fmt.Sprintf("<p>%s</p>", text)
				}
			}
		}
	}

	var headerHtml string
	var descriptionHtml string
	if isArticle {
		headerHtml = ""
		descriptionHtml = articleBodyHtml
	} else {
		headerHtml = headerImageHtml
		desc, _ := link["description"].(string)
		desc = xhhReplaceEmoji(html.EscapeString(desc))
		desc = strings.ReplaceAll(desc, "\n", "<br>")
		descriptionHtml = desc
	}

	avatarDecoration := ""
	if decMap, ok := user["avatar_decoration"].(map[string]any); ok {
		if src, ok := decMap["src_url"].(string); ok && src != "" {
			avatarDecoration = fmt.Sprintf(`<img class="decoration" src="%s" alt="">`, html.EscapeString(src))
		}
	}

	medalIcons := ""
	if medals, ok := user["medals"].([]any); ok {
		for _, mObj := range medals {
			if m, ok := mObj.(map[string]any); ok {
				if wear, ok := m["wear"].(float64); ok && wear == 1 {
					if imgUrl, ok := m["img_url"].(string); ok && imgUrl != "" {
						medalIcons += fmt.Sprintf(`<img class="medal-icon" src="%s" alt="">`, html.EscapeString(imgUrl))
					}
				}
			}
		}
	}

	level := 0
	if levelInfo, ok := user["level_info"].(map[string]any); ok {
		if lv, ok := levelInfo["level"].(float64); ok {
			level = int(lv)
		}
	}

	contentTags := ""
	if tags, ok := link["content_tags"].([]any); ok {
		for _, tagObj := range tags {
			if tag, ok := tagObj.(map[string]any); ok {
				tagText, _ := tag["text"].(string)
				tagIcon, _ := tag["icon"].(string)
				bgColor := "#F3F4F5"
				textColor := "#14191E"
				if bg, ok := tag["bg_color"].(string); ok && bg != "" {
					bgColor = xhhParseLightColor(bg, "#F3F4F5")
				}
				if tc, ok := tag["text_color"].(string); ok && tc != "" {
					textColor = xhhParseLightColor(tc, "#14191E")
				}
				contentTags += fmt.Sprintf(`<span class="tag" style="background:%s;color:%s;">`, bgColor, textColor)
				if tagIcon != "" {
					contentTags += fmt.Sprintf(`<img src="%s" alt="">`, html.EscapeString(tagIcon))
				}
				contentTags += fmt.Sprintf(`%s</span>`, html.EscapeString(tagText))
			}
		}
	}

	createAt, _ := link["create_at"].(float64)
	timeText := xhhFormatTime(int64(createAt))

	commentNum, _ := link["comment_num"].(float64)
	favourCount, _ := link["favour_count"].(float64)
	linkAwardNum, _ := link["link_award_num"].(float64)
	click, _ := link["click"].(float64)
	ipLocation, _ := link["ip_location"].(string)
	
	avatarUrl, _ := user["avatar"].(string)
	username, _ := user["username"].(string)
	title, _ := link["title"].(string)

	htmlTpl := xhhHtmlTemplate
	htmlTpl = strings.Replace(htmlTpl, "{{HEADER_IMAGES}}", headerHtml, -1)
	htmlTpl = strings.Replace(htmlTpl, "{{AVATAR_URL}}", html.EscapeString(avatarUrl), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{AVATAR_DECORATION}}", avatarDecoration, -1)
	htmlTpl = strings.Replace(htmlTpl, "{{USERNAME}}", html.EscapeString(username), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{MEDAL_ICONS}}", medalIcons, -1)
	htmlTpl = strings.Replace(htmlTpl, "{{LEVEL}}", fmt.Sprintf("%d", level), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{TITLE}}", html.EscapeString(title), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{DESCRIPTION}}", descriptionHtml, -1)
	htmlTpl = strings.Replace(htmlTpl, "{{CONTENT_TAGS}}", contentTags, -1)
	htmlTpl = strings.Replace(htmlTpl, "{{TIME_TEXT}}", html.EscapeString(timeText), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{IP_LOCATION}}", html.EscapeString(ipLocation), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{COMMENT_NUM}}", fmt.Sprintf("%d", int(commentNum)), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{FAVOUR_COUNT}}", fmt.Sprintf("%d", int(favourCount)), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{LINK_AWARD_NUM}}", fmt.Sprintf("%d", int(linkAwardNum)), -1)
	htmlTpl = strings.Replace(htmlTpl, "{{CLICK}}", fmt.Sprintf("%d", int(click)), -1)

	return htmlTpl, nil
}

func (r *Renderer) SaveXHHDynamic(ctx context.Context, prefix, jsonText string) (string, error) {
	util.Log("DBG", "Render", "开始解析小黑盒HTML模板...")
	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonText), &raw); err != nil {
		return "", fmt.Errorf("parse xhh json error: %v", err)
	}

	status, _ := raw["status"].(string)
	if status != "ok" {
		msg, _ := raw["msg"].(string)
		return "", fmt.Errorf("xhh api failed: %s", msg)
	}

	result, ok := raw["result"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("result is not map")
	}

	htmlContent, err := buildXHHHtml(result)
	if err != nil {
		return "", err
	}

	dataURL := "data:text/html;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(htmlContent))
	return r.SaveURLScreenshot(ctx, prefix, dataURL, ".post-card", 0)
}
