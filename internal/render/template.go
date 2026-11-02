package render

import (
	"bytes"
	"dd_screen_go/internal/platform"
	"dd_screen_go/internal/util"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	templates "dd_screen_go/template"
)

type LiveTemplateData struct {
	Info          platform.LiveInfo
	AvatarBase64  string
	CoverBase64   string
	QRBase64      string
	Duration      string
	DurationLabel string
	IsLive        bool
	Timestamp     string
	Options       CardOptions
	GeneratedAt   string
}

type BiliRichTextNode struct {
	Type      string
	Text      string
	JumpURL   string
	Rid       string
	IconURL   string
	EmojiSize int
}

type BiliVoteOption struct {
	Desc    string
	Count   int
	Percent float64
}

type BiliDynamicSimple struct {
	AuthorName   string
	AuthorAvatar string
	PubTime      string

	HasFanCard   bool
	FanCardName  string
	FanCardNum   string
	FanCardColor string
	FanCardBg    string

	Topic    string
	Title    string
	Text     string
	RichText []BiliRichTextNode

	Images []string

	HasVote     bool
	VoteDesc    string
	VoteJoinNum int
	VoteOptions []BiliVoteOption

	IsForward bool
	Forward   *BiliDynamicSimple
}

type DynamicTemplateData struct {
	Platform    string
	RawURL      string
	ImageBase64 string
	RawData     any
	BiliData    *BiliDynamicSimple
	Timestamp   string
	GeneratedAt string
	Expand      bool
	AtCard      bool
	LinkQr      bool
}

// templateFuncs provides helper functions available in all .tmpl templates.
var templateFuncs = template.FuncMap{
	// contains reports whether substr is within s.
	"contains": func(s, substr string) bool {
		return strings.Contains(s, substr)
	},
	// split splits s on sep and returns the resulting slice.
	"split": func(s, sep string) []string {
		return strings.Split(s, sep)
	},
	// trimSpace trims leading and trailing whitespace.
	"trimSpace": strings.TrimSpace,
}

type TemplateManager struct {
	liveTemplates    map[string]string // maps key to filepath
	dynamicTemplates map[string]string
}

func NewTemplateManager() *TemplateManager {
	tm := &TemplateManager{
		liveTemplates:    make(map[string]string),
		dynamicTemplates: make(map[string]string),
	}
	tm.LoadTemplates()
	return tm
}

func (tm *TemplateManager) LoadTemplates() {
	dir := "template"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		_ = os.MkdirAll(dir, 0755)
	}

	// 自动释放内置的默认模板文件（如果本地不存在的话）
	embeddedFiles, err := templates.FS.ReadDir(".")
	if err == nil {
		for _, ef := range embeddedFiles {
			if ef.IsDir() {
				continue
			}
			localPath := filepath.Join(dir, ef.Name())
			if _, err := os.Stat(localPath); os.IsNotExist(err) {
				if content, err := templates.FS.ReadFile(ef.Name()); err == nil {
					_ = os.WriteFile(localPath, content, 0644)
					util.Log("INF", "Template", "自动生成内置默认模板文件: %s", ef.Name())
				}
			}
		}
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		util.Log("ERR", "Template", "Failed to read template directory: %v", err)
		return
	}

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".tmpl") {
			continue
		}

		name := f.Name()
		nameLower := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))

		filepathStr := filepath.Join(dir, f.Name())

		// Just parse once to check for syntax errors during startup, but we don't store it.
		_, err := template.New(name).Funcs(templateFuncs).ParseFiles(filepathStr)
		if err != nil {
			util.Log("ERR", "Template", "Failed to parse template %s: %v", f.Name(), err)
			continue
		}

		reLive := regexp.MustCompile(`^([a-z0-9]*)live(\d+)$`)
		reDynamic := regexp.MustCompile(`^([a-z0-9]*)dynamic(\d+)$`)

		if m := reLive.FindStringSubmatch(nameLower); len(m) > 0 {
			platform := m[1]
			variant, _ := strconv.Atoi(m[2])
			key := fmt.Sprintf("%s_%d", platform, variant)
			tm.liveTemplates[key] = filepathStr
			util.Log("INF", "Template", "Loaded Live template (Platform: %s, Variant: %d) from %s", platform, variant, f.Name())
		} else if m := reDynamic.FindStringSubmatch(nameLower); len(m) > 0 {
			platform := m[1]
			variant, _ := strconv.Atoi(m[2])
			key := fmt.Sprintf("%s_%d", platform, variant)
			tm.dynamicTemplates[key] = filepathStr
			util.Log("INF", "Template", "Loaded Dynamic template (Platform: %s, Variant: %d) from %s", platform, variant, f.Name())
		}
	}
}

func (tm *TemplateManager) HasLiveVariant(platform string, v int) bool {
	platform = strings.ToLower(platform)
	if _, ok := tm.liveTemplates[fmt.Sprintf("%s_%d", platform, v)]; ok {
		return true
	}
	// Fallback to generic
	_, ok := tm.liveTemplates[fmt.Sprintf("_%d", v)]
	return ok
}

func (tm *TemplateManager) RenderLiveVariant(platform string, v int, data LiveTemplateData) (string, error) {
	platform = strings.ToLower(platform)
	fp, ok := tm.liveTemplates[fmt.Sprintf("%s_%d", platform, v)]
	if !ok {
		fp, ok = tm.liveTemplates[fmt.Sprintf("_%d", v)]
		if !ok {
			return "", fmt.Errorf("live template variant %d for platform %s not found", v, platform)
		}
	}

	// Parse on every render for hot-reloading
	t, err := template.New(filepath.Base(fp)).Funcs(templateFuncs).ParseFiles(fp)
	if err != nil {
		return "", fmt.Errorf("failed to parse template file %s: %v", fp, err)
	}

	data.GeneratedAt = time.Now().Format("2006-01-02 15:04:05")
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (tm *TemplateManager) HasDynamicVariant(platform string, v int) bool {
	platform = strings.ToLower(platform)
	if _, ok := tm.dynamicTemplates[fmt.Sprintf("%s_%d", platform, v)]; ok {
		return true
	}
	_, ok := tm.dynamicTemplates[fmt.Sprintf("_%d", v)]
	return ok
}

func (tm *TemplateManager) RenderDynamicVariant(platform string, v int, data DynamicTemplateData) (string, error) {
	platform = strings.ToLower(platform)
	fp, ok := tm.dynamicTemplates[fmt.Sprintf("%s_%d", platform, v)]
	if !ok {
		fp, ok = tm.dynamicTemplates[fmt.Sprintf("_%d", v)]
		if !ok {
			return "", fmt.Errorf("dynamic template variant %d for platform %s not found", v, platform)
		}
	}

	t, err := template.New(filepath.Base(fp)).Funcs(templateFuncs).ParseFiles(fp)
	if err != nil {
		return "", fmt.Errorf("failed to parse template file %s: %v", fp, err)
	}

	data.GeneratedAt = time.Now().Format("2006-01-02 15:04:05")
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (tm *TemplateManager) GetLiveVariants() []string {
	var variants []string
	for k := range tm.liveTemplates {
		variants = append(variants, k)
	}
	return variants
}

func (tm *TemplateManager) GetDynamicVariants() []string {
	var variants []string
	for k := range tm.dynamicTemplates {
		variants = append(variants, k)
	}
	return variants
}
