package webui

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var tableExtension = extension.Table

// linkSanitizer 把 markdown 里危险协议的链接目标降级为 "#"：
// 允许相对路径（缺陷截图等站内链接）、http(s)、mailto 与页内锚点；
// javascript:/data:/vbscript: 等一律清除。goldmark 默认不渲染内嵌
// 原始 HTML（unsafe=false），加上这里的最小 scheme 白名单即可安全。
type linkSanitizer struct{}

func (linkSanitizer) Transform(node *ast.Document, _ text.Reader, _ parser.Context) {
	ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch ln := n.(type) {
		case *ast.Link:
			if !safeDest(ln.Destination) {
				ln.Destination = []byte("#")
			}
		case *ast.Image:
			if !safeDest(ln.Destination) {
				ln.Destination = []byte("#")
			}
		}
		// AutoLink 不处理：其解析器只产生 http/https/mailto 协议，无注入面。
		return ast.WalkContinue, nil
	})
}

// safeDest 报告链接目标是否允许：相对路径/锚点/http(s)/mailto。
func safeDest(dest []byte) bool {
	d := strings.TrimSpace(string(dest))
	if d == "" || strings.HasPrefix(d, "#") || strings.HasPrefix(d, "/") {
		return true
	}
	scheme, _, found := strings.Cut(d, ":")
	if !found {
		return true // 纯相对路径
	}
	switch strings.ToLower(scheme) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

var bugMarkdown = goldmark.New(
	goldmark.WithExtensions(tableExtension),
	goldmark.WithParserOptions(parser.WithASTTransformers(
		util.Prioritized(linkSanitizer{}, 99),
	)),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// renderMarkdown 渲染统一的 markdown 文档为安全 HTML（服务端转义，无 raw HTML）。
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := bugMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<p>" + template.HTMLEscapeString(src) + "</p>")
	}
	return template.HTML(buf.String())
}
