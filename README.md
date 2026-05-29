# DD_SCREEN GO

这是把原 `DD_Screen` C# ASP.NET 项目重构到 Go 后的版本。
使用 Go + `chromedp` (Chrome DevTools Protocol) 实现了全平台无头（Headless）网页截图和数据采集服务，专门搭配 **DDBOT** 使用，提供 B站、抖音、斗鱼、虎牙、A站、微博、推特(X) 等各大平台的动态/直播切图功能。

## ✨ 核心特性与优化 (v1.8)

相较于原版的 C# CefSharp 实现，Go 语言版本进行了深度重构与诸多体验优化：

1. **真正的 Headless 渲染**：彻底告别 CefSharp，采用 `chromedp`，无需图形界面支持，更省内存，更适合部署。
2. **完美透明圆角生成**：内建 Go 原生图像处理，抛弃了依赖背景色抠图的老方案，真正实现带 Alpha 通道的抗锯齿圆角切割。
3. **B站动态长图智能展开**：修复 B 站新版 `opus` 动态中，单张极长图因 `max-height` 被裁切的问题（`expand=true` 时自动计算比例并强制将长图完整拼接展开）。
4. **反重定向 WAF 绕过**：优化 B 站动态路由判断逻辑。针对 B 站非专栏图文跳转 `t.bilibili.com` 触发防刷风控（导致大量 412/404）的问题，实现了前端直连，截图速度缩短至 1~3 秒内！
5. **自动生成的 API 文档**：内置了完整的 Swagger UI (`/swagger/index.html`)，所有新老接口的参数全部对齐并做了详尽注释说明，方便手动调试。

## 🚀 运行与启动

**源码运行：**
```powershell
cd "DD_SCREEN GO"
go run .\cmd\dd-screen-go
```

**编译发布版：**
```powershell
go build -o "DDScreenGO v1.4.exe" cmd/dd-screen-go/main.go
```

启动后访问 API 测试与调试页：
```text
http://127.0.0.1:7000/swagger/index.html
```

## ⚙️ 配置说明

配置文件为 `appsettings.json`：

```json
{
  "ListenAddr": "127.0.0.1:7000",
  "ChromePath": "",
  "Headless": true,
  "Debug": false,
  "LogLevel": "info"
}
```

- `ListenAddr`: HTTP 监听地址，默认 `127.0.0.1:7000`
- `ChromePath`: Chrome/Edge/Chromium 的可执行文件绝对路径，**留空时系统会自动在常见路径查找**。
- `Headless`: 是否使用无头模式（推荐开启）。
- `Debug`: 开启调试模式（在 Swagger 中会显示被标记为隐藏或废弃的接口）。
- `LogLevel`: 日志输出级别。支持 `debug`, `info`, `warn`, `error`。设为 `error` 时可大幅减少控制台日志输出。

生成的最终截图全部缓存在 `ScreenShotImg/` 目录下，外部可通过类似 `http://127.0.0.1:7000/ScreenShotImg/xxxx.png` 进行访问。
