package httpapi

import (
	"fmt"
	"sort"
	"strings"
)

type docParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Default     any
	Enum        []any
}

type docEndpoint struct {
	Tag         string
	Method      string
	Path        string
	Summary     string
	Description string
	Disabled    bool
	Params      []docParam
	RequestBody bool
}

func openAPIDocument(debug bool) map[string]any {
	paths := map[string]any{}
	tagsSeen := map[string]bool{}
	tags := []map[string]string{}

	for _, ep := range allDocEndpoints() {
		if debug && ep.Disabled {
			continue
		}
		if !tagsSeen[ep.Tag] {
			tagsSeen[ep.Tag] = true
			tags = append(tags, map[string]string{"name": ep.Tag})
		}
		if paths[ep.Path] == nil {
			paths[ep.Path] = map[string]any{}
		}
		item := paths[ep.Path].(map[string]any)
		item[strings.ToLower(ep.Method)] = operationObject(ep)
	}

	return map[string]any{
		"openapi": "3.0.0",
		"info": map[string]any{
			"title":       "DDBOT Rendering Service",
			"version":     "v1",
			"description": "一枝独秀不是春,万紫千红春满园。\n\n**搭配 DDBOT 使用**\n\nDDBOT 交流群:**980848391**",
		},
		"tags":  tags,
		"paths": paths,
	}
}

func operationObject(ep docEndpoint) map[string]any {
	params := []map[string]any{}
	for _, p := range ep.Params {
		schema := map[string]any{"type": p.Type}
		if p.Default != nil {
			schema["default"] = p.Default
		}
		if len(p.Enum) > 0 {
			schema["enum"] = p.Enum
		}
		params = append(params, map[string]any{
			"name":        p.Name,
			"in":          "query",
			"description": p.Description,
			"required":    p.Required,
			"schema":      schema,
		})
	}

	op := map[string]any{
		"tags":        []string{ep.Tag},
		"summary":     ep.Summary,
		"description": ep.Description,
		"parameters":  params,
		"responses": map[string]any{
			"200": map[string]any{"description": "OK"},
		},
	}
	if ep.RequestBody {
		op["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{"type": "object", "additionalProperties": true},
				},
			},
		}
	}
	return op
}

func allDocEndpoints() []docEndpoint {
	var eps []docEndpoint
	add := func(tag, path, summary string, disabled bool, params []docParam, methods ...string) {
		for _, method := range methods {
			eps = append(eps, docEndpoint{
				Tag:      tag,
				Method:   method,
				Path:     path,
				Summary:  summary,
				Disabled: disabled,
				Params:   params,
			})
		}
	}
	addBody := func(tag, path, summary string, disabled bool, params []docParam, methods ...string) {
		for _, method := range methods {
			eps = append(eps, docEndpoint{
				Tag:         tag,
				Method:      method,
				Path:        path,
				Summary:     summary,
				Disabled:    disabled,
				Params:      params,
				RequestBody: true,
			})
		}
	}

	liveCommon := []docParam{
		pInt("live_state", "是否直播 0 上播 1 下播", 0),
		pBool("qr", "是否显示二维码", false),
		pInt("view", "显示模式 0 桌面端 1 移动端", 0),
		pStringDefault("model_order", "模块顺序 1.封面 2.简介 3.二维码", "1,2,3"),
		pStringDefault("tips", "自定义联名", ""),
		pStringDefault("timestamp", "时间戳10/13位", ""),
	}
	liveWithRoom := append([]docParam{pRequired("roomid", "房间号/直播间链接")}, liveCommon...)
	liveWithUID := append([]docParam{pRequired("uid", "UID/直播间链接")}, liveCommon...)

	add("Acfun", "/api/Acfun/Dynamic", "动态转图", true, []docParam{pRequired("url", "文章链接，如：https://www.acfun.cn/a/ac48034531")}, "POST", "GET")
	add("Acfun", "/api/Acfun/Live", "直播转图", true, liveWithUID, "POST", "GET")
	add("Acfun", "/api/Acfun/Live2", "直播转图 2", true, liveWithUID, "GET", "POST")

	add("Bili", "/api/Bili/BiliQRCodeLogin", "B站扫码登录", true, nil, "GET")
	add("Bili", "/api/Bili/GetBiliCookie", "获取B站Cookie信息", true, nil, "GET")
	addBody("Bili", "/api/Bili/List", "订阅转图", false, []docParam{
		pStringDefault("bg", "背景图片路径或URL（支持：本地/网络图）", ""),
		pBool("text_char", "false-长文本超长缩小 true-长文本超长省略", false),
	}, "POST")
	add("Bili", "/api/Bili/Dynamic", "动态转图（风林火山提供思路）", false, []docParam{
		pRequired("url", "https://www.bilibili.com/opus/******** 或 https://t.bilibili.com/********"),
		pBool("expand", "是否强制展开所有图片", false),
		pBool("column", "是否强制解析专栏", false),
	}, "GET", "POST")
	add("Bili", "/api/Bili/Live", "直播转图", false, []docParam{
		pRequired("roomid", "房间号/直播间链接"),
		pInt("live_state", "是否直播 0 上播 1 下播", 0),
		pBool("content", "是否显示公告", true),
		pBool("qr", "是否显示二维码", false),
		pInt("view", "显示模式 0 桌面端 1 移动端", 0),
		pStringDefault("model_order", "模块顺序 1.封面 2.简介 3.公告 4.二维码", "1,2,3,4"),
		pStringDefault("tips", "自定义联名", ""),
		pStringDefault("timestamp", "时间戳10/13位", ""),
		pBool("standalone", "单跑独立播放器", false),
	}, "GET", "POST")
	add("Bili", "/api/Bili/Live2", "直播转图 (凛雅提供样式)", false, []docParam{
		pRequired("roomid", "房间号/直播间链接"),
		pInt("live_state", "是否直播 0 上播 1 下播", 0),
		pBool("qr", "是否显示二维码", false),
		pInt("view", "显示模式 0 桌面端 1 移动端", 0),
		pStringDefault("model_order", "模块顺序 1.封面 2.简介 3.二维码", "1,2,3"),
		pStringDefault("tips", "自定义联名", ""),
		pStringDefault("timestamp", "时间戳10/13位", ""),
		pBool("standalone", "单跑独立播放器", false),
	}, "GET", "POST")
	add("Bili", "/api/Bili/Live3", "直播转图 (华芙提供样式)", false, []docParam{
		pRequired("roomid", "房间号/直播间链接"),
		pInt("live_state", "是否直播 0 上播 1 下播", 0),
		pStringDefault("tips", "自定义联名后缀", ""),
		pStringDefault("timestamp", "时间戳10/13位", ""),
	}, "GET", "POST")

	add("Douyin", "/api/Douyin/DouyinQRCodeLogin", "抖音扫码登录", true, nil, "GET")
	add("Douyin", "/api/Douyin/Live", "直播转图 1", true, liveWithRoom, "GET", "POST")
	add("Douyin", "/api/Douyin/Live2", "直播转图 2", true, liveWithRoom, "GET", "POST")
	add("Douyin", "/api/Douyin/Live3", "直播间查询截图 (可爱样式)", true, []docParam{
		pRequired("roomid", "房间号/直播间链接"),
		pBool("qr", "是否显示二维码", false),
		pInt("live_state", "是否固定直播状态 (0=不修改 1=开播 2=下播)", 0),
	}, "GET", "POST")

	add("Douyu", "/api/Douyu/Live", "直播转图 1", true, liveWithRoom, "GET", "POST")
	add("Douyu", "/api/Douyu/Live2", "直播转图 2", true, liveWithRoom, "GET", "POST")

	add("Huya", "/api/Huya/Live", "直播转图 1", true, append([]docParam{pRequired("roomid", "房间号/直播间链接（https://www.huya.com/xxxx）")}, liveCommon...), "GET", "POST")
	add("Huya", "/api/Huya/Live2", "直播转图 2", true, append([]docParam{pRequired("roomid", "房间号/直播间链接（https://www.huya.com/xxxx）")}, liveCommon...), "GET", "POST")

	add("Nico", "/api/Nico/Live", "直播转图", true, append([]docParam{pRequired("liveId", "直播间ID（lv******）或完整链接")}, withoutTimestamp(liveCommon)...), "GET", "POST")
	add("Nico", "/api/Nico/Live2", "直播转图 2", true, append([]docParam{pRequired("liveId", "直播间ID（lv******）或完整链接")}, withoutTimestamp(liveCommon)...), "GET", "POST")

	add("Twitch", "/api/Twitch/Live", "直播转图", true, append([]docParam{pRequired("login", "Twitch用户名")}, withoutTimestamp(liveCommon)...), "GET", "POST")
	add("Twitch", "/api/Twitch/Live2", "直播转图 2", true, append([]docParam{pRequired("login", "Twitch用户名")}, withoutTimestamp(liveCommon)...), "GET", "POST")

	add("Weibo", "/api/Weibo/WeiboQRCodeLogin", "微博扫码登录", true, nil, "GET")
	add("Weibo", "/api/Weibo/RefreshWeiboCookie", "手动刷新微博Cookie", true, nil, "GET")
	add("Weibo", "/api/Weibo/GetWeiboCookie", "获取微博Cookie信息", true, nil, "GET")
	add("Weibo", "/api/Weibo/GetMobileProfile", "移动端 获取用户资料", true, []docParam{pIntRequired("uid", "微博UID")}, "GET")
	add("Weibo", "/api/Weibo/GetMobileCards", "移动端 获取微博列表", true, []docParam{pIntRequired("uid", "微博UID")}, "GET")
	add("Weibo", "/api/Weibo/Dynamic", "动态转图", true, []docParam{pRequired("url", "https://weibo.com/********/********")}, "GET", "POST")

	add("X", "/api/X/Dynamic", "截取X推文（官方）", true, []docParam{pRequired("url", "推文链接 (https://x.com/username/status/1234567890)")}, "GET", "POST")
	add("X", "/api/X/NitterPoast", "截取X推文（镜像 1）", true, []docParam{pRequired("url", "推文链接 (https://x.com/username/status/1234567890)")}, "GET", "POST")
	add("X", "/api/X/NitterNet", "截取X推文（镜像 2）", true, []docParam{pRequired("url", "推文链接 (https://x.com/username/status/1234567890)")}, "GET", "POST")

	defaultUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	add("XHH", "/api/XHH/GetTokenID", "获取x_xhh_tokenid", true, []docParam{pStringDefault("UserAgent", "UA头", defaultUA)}, "GET", "POST")
	add("XHH", "/api/XHH/GetDeviceID", "获取device_id", true, []docParam{pStringDefault("UserAgent", "UA头", defaultUA)}, "GET", "POST")
	add("XHH", "/api/XHH/GetSmidV2", "获取smidV2", true, nil, "GET", "POST")
	add("XHH", "/api/XHH/GetProfileEvents", "获取用户动态列表（x_xhh_tokenid）", true, xhhProfileParams("token", "Cookie 从 GetTokenID 获取", defaultUA), "GET", "POST")
	add("XHH", "/api/XHH/GetProfileEventsV2", "获取用户动态列表（smidV2）", true, xhhProfileParams("smidV2", "Cookie 从 GetSmidV2 获取", defaultUA), "GET", "POST")
	add("XHH", "/api/XHH/Dynamic", "帖子转图", true, []docParam{
		pRequired("url", "分享链接 含有linkId"),
		pRequired("token", "Cookie 从 GetTokenID 获取"),
		pRequired("deviceId", "设备ID 从 GetDeviceID 获取"),
		pStringDefault("device_info", "设备头", "Chrome"),
		pStringDefault("UA", "UA头", defaultUA),
	}, "GET", "POST")
	add("XHH", "/api/XHH/Verify", "打开小黑盒验证页面（返回可交互的远程操作页面）", true, []docParam{pRequired("token", "x_xhh_tokenid 的值")}, "GET", "POST")
	add("XHH", "/api/XHH/Verify/Screenshot", "获取验证页面截图", true, nil, "GET")
	add("XHH", "/api/XHH/Verify/Click", "转发鼠标点击到验证页面", true, []docParam{pIntRequired("x", "X 坐标"), pIntRequired("y", "Y 坐标")}, "POST")
	add("XHH", "/api/XHH/Verify/Close", "关闭验证页面", true, nil, "POST")

	add("Youtube", "/api/Youtube/Card", "直播/直播预告/视频 转图", true, []docParam{
		pRequired("url", "视频/直播预告/直播 URL (https://www.youtube.com/watch?v=xxxxx)"),
		pBool("qr", "是否显示二维码", true),
		pInt("view", "显示模式 0=PC 1=移动", 0),
		pStringDefault("tips", "自定义提示文本", ""),
		pInt("live_state", "样式风格：0=亮色(默认), 1=暗色, 2=自动(直播亮/其他暗)", 0),
	}, "GET", "POST")

	return eps
}

func pRequired(name, desc string) docParam {
	return docParam{Name: name, Type: "string", Description: desc, Required: true}
}

func pStringDefault(name, desc, def string) docParam {
	return docParam{Name: name, Type: "string", Description: desc, Default: def}
}

func pBool(name, desc string, def bool) docParam {
	return docParam{Name: name, Type: "boolean", Description: desc, Default: def, Enum: []any{true, false}}
}

func pInt(name, desc string, def int) docParam {
	return docParam{Name: name, Type: "integer", Description: desc, Default: def}
}

func pIntRequired(name, desc string) docParam {
	return docParam{Name: name, Type: "integer", Description: desc, Required: true}
}

func withoutTimestamp(params []docParam) []docParam {
	out := []docParam{}
	for _, p := range params {
		if p.Name != "timestamp" {
			out = append(out, p)
		}
	}
	return out
}

func xhhProfileParams(tokenName, tokenDesc, defaultUA string) []docParam {
	return []docParam{
		pRequired(tokenName, tokenDesc),
		pRequired("userid", "用户的UID"),
		pRequired("deviceId", "设备ID 从 GetDeviceID 获取"),
		pStringDefault("device_info", "设备头 Chrome", "Chrome"),
		pStringDefault("UA", "UA头", defaultUA),
	}
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8">
    <title>DDBOT Rendering Service</title>
    <link rel="stylesheet" type="text/css" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui.css" />
    <link rel="icon" type="image/png" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/favicon-32x32.png" sizes="32x32" />
    <style>
      html
      {
        box-sizing: border-box;
        overflow: -y-scroll;
      }

      *,
      *:before,
      *:after
      {
        box-sizing: inherit;
      }

      body
      {
        margin:0;
        background: #fafafa;
      }
    </style>
  </head>

  <body>
    <div id="swagger-ui"></div>

    <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui-bundle.js" charset="UTF-8"> </script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui-standalone-preset.js" charset="UTF-8"> </script>
    <script>
    window.onload = function() {
      // Begin Swagger UI call region
      const ui = SwaggerUIBundle({
        url: "/swagger/v1/swagger.json",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout"
      });
      // End Swagger UI call region
      window.ui = ui;
    };
  </script>
  </body>
</html>`

func sortedDocPaths(debug bool) []string {
	paths := []string{}
	for p := range openAPIDocument(debug)["paths"].(map[string]any) {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func _docDebugString(debug bool) string {
	return fmt.Sprint(sortedDocPaths(debug))
}
