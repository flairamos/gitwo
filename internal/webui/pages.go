package webui

import (
	"html/template"
	"strconv"
	"strings"
	"time"

	"gitwo/internal/model"
	"gitwo/internal/store"
)

var tmplFuncs = template.FuncMap{
	"ftime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"fdur": func(d time.Duration) string {
		if d <= 0 {
			return "-"
		}
		return d.Round(time.Millisecond).String()
	},
	"durOf": func(start, end time.Time) time.Duration {
		if start.IsZero() || end.IsZero() {
			return 0
		}
		return end.Sub(start)
	},
	"shal": func(sha string) string {
		if len(sha) > 7 {
			return sha[:7]
		}
		return sha
	},
	"orgsOf": func(orgs []store.Org, user string) string {
		var out []string
		for _, o := range orgs {
			for _, m := range o.Members {
				if m == user {
					out = append(out, o.Name)
					break
				}
			}
		}
		if len(out) == 0 {
			return "-"
		}
		return strings.Join(out, ", ")
	},
	"ownerLabel": func(owner string) string {
		_, name, _ := strings.Cut(owner, ":")
		return name
	},
	"initials": func(s string) string {
		r := []rune(strings.TrimSpace(s))
		if len(r) == 0 {
			return "?"
		}
		return strings.ToUpper(string(r[0]))
	},
	"avacolor": func(s string) string {
		colors := []string{"#1f6feb", "#238636", "#9e6a03", "#8e44ad", "#b3591c", "#0d8a8a"}
		var h uint32
		for _, c := range s {
			h = h*31 + uint32(c)
		}
		return colors[h%uint32(len(colors))]
	},
	"hsize":         humanSize,
	"bugStatusText": store.BugStatusText,
	"bugStatusClass": func(s string) string {
		switch s {
		case store.BugOpen:
			return "failed"
		case store.BugFixed:
			return "running"
		default:
			return "success"
		}
	},
	"severityText": func(s string) string {
		switch s {
		case store.SeverityBlocker:
			return "严重"
		case store.SeverityMajor:
			return "一般"
		default:
			return "轻微"
		}
	},
	"reqStatusText":  store.ReqStatusText,
	"renderMarkdown": renderMarkdown,
	"reqStatusClass": func(s string) string {
		switch s {
		case store.ReqPending:
			return "pending"
		case store.ReqReview:
			return "running"
		default:
			return "success"
		}
	},
	"severityClass": func(s string) string {
		switch s {
		case store.SeverityBlocker:
			return "failed"
		case store.SeverityMajor:
			return "running"
		default:
			return ""
		}
	},
	"blobJS": func() template.JS { return template.JS(blobJS) }, // script 上下文里 JS 类型原样输出
	"oct":    octIcon(16),
	"octBig": octIcon(28),
}

// humanSize renders a byte count for file listings ("-" for unknown sizes).
func humanSize(n int64) string {
	switch {
	case n < 0:
		return "-"
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KiB"
	case n < 1024*1024*1024:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MiB"
	default:
		return strconv.FormatFloat(float64(n)/(1024*1024*1024), 'f', 1, 64) + " GiB"
	}
}

// octIcon renders GitHub Octicons (MIT) as inline SVG so the whole UI stays
// self-contained: no external assets, color follows the theme via currentColor.
func octIcon(size int) func(string) template.HTML {
	paths := map[string]string{
		"repo":   `<path d="M2 2.5A2.5 2.5 0 0 1 4.5 0h8.75a.75.75 0 0 1 .75.75v12.5a.75.75 0 0 1-.75.75h-2.5a.75.75 0 0 1 0-1.5h1.75v-2h-8a1 1 0 0 0-.714 1.7.75.75 0 1 1-1.072 1.05A2.495 2.495 0 0 1 2 11.5Zm10.5-1h-8a1 1 0 0 0-1 1v6.708A2.486 2.486 0 0 1 4.5 9h8ZM5 12.25a.25.25 0 0 1 .25-.25h3.5a.25.25 0 0 1 .25.25v3.25a.25.25 0 0 1-.4.2l-1.45-1.087a.249.249 0 0 0-.2 0L8.5 15.7a.25.25 0 0 1-.4-.2Z"/>`,
		"file":   `<path d="M2 1.75C2 .784 2.784 0 3.75 0h6.586c.464 0 .909.184 1.237.513l2.914 2.914c.329.328.513.773.513 1.237v9.586A1.75 1.75 0 0 1 13.25 16h-9.5A1.75 1.75 0 0 1 2 14.25Zm1.75-.25a.25.25 0 0 0-.25.25v12.5c0 .138.112.25.25.25h9.5a.25.25 0 0 0 .25-.25V6h-2.75A1.75 1.75 0 0 1 9 4.25V1.5Zm6.75.062V4.25c0 .138.112.25.25.25h2.688l-.011-.013-2.914-2.914-.013-.011Z"/>`,
		"dir":    `<path d="M1.75 1A1.75 1.75 0 0 0 0 2.75v10.5C0 14.216.784 15 1.75 15h12.5A1.75 1.75 0 0 0 16 13.25v-8.5A1.75 1.75 0 0 0 14.25 3H7.5a.25.25 0 0 1-.2-.1l-.9-1.2C6.07 1.26 5.55 1 5 1H1.75Z"/>`,
		"commit": `<path d="M11.93 8.5a4.002 4.002 0 0 1-7.86 0H.75a.75.75 0 0 1 0-1.5h3.32a4.002 4.002 0 0 1 7.86 0h3.32a.75.75 0 0 1 0 1.5Zm-1.43-.75a2.5 2.5 0 1 0-5 0 2.5 2.5 0 0 0 5 0Z"/>`,
		"check":  `<path d="M8 16A8 8 0 1 1 8 0a8 8 0 0 1 0 16Zm3.78-9.72a.751.751 0 0 0-.018-1.042.751.751 0 0 0-1.042-.018L6.75 9.19 5.28 7.72a.751.751 0 0 0-1.042.018.751.751 0 0 0-.018 1.042l2 2a.75.75 0 0 0 1.06 0Z"/>`,
		"x":      `<path d="M2.343 13.657A8 8 0 1 1 13.658 2.343 8 8 0 0 1 2.343 13.657ZM6.03 4.97a.751.751 0 0 0-1.042.018.751.751 0 0 0-.018 1.042L6.94 8 4.97 9.97a.749.749 0 0 0 .326 1.275.749.749 0 0 0 .734-.215L8 11.06l1.97 1.97a.749.749 0 0 0 1.275-.326.749.749 0 0 0-.215-.734L9.06 10l1.97-1.97a.749.749 0 0 0-.326-1.275.749.749 0 0 0-.734.215L8 8.94Z"/>`,
		"dot":    `<path d="M8 4a4 4 0 1 1 0 8 4 4 0 0 1 0-8Z"/>`,
		"clock":  `<path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0ZM1.5 8a6.5 6.5 0 1 0 13 0 6.5 6.5 0 0 0-13 0Zm7-3.25v2.992l2.028.812a.75.75 0 0 1-.557 1.392l-2.5-1A.751.751 0 0 1 7 8.25v-3.5a.75.75 0 0 1 1.5 0Z"/>`,
		"play":   `<path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0ZM1.5 8a6.5 6.5 0 1 0 13 0 6.5 6.5 0 0 0-13 0Zm4.879-2.773 4.36 2.83a.5.5 0 0 1 0 .843l-4.36 2.83a.5.5 0 0 1-.769-.421V5.148a.5.5 0 0 1 .769-.42Z"/>`,
		"gear":   `<path d="M8 0a8.2 8.2 0 0 1 .701.031C9.444.095 9.99.645 10.16 1.29l.288 1.107c.018.067.079.158.212.224.231.114.454.243.668.386.123.082.233.09.299.071l1.103-.303c.644-.176 1.392.021 1.82.63.27.385.506.792.704 1.218.315.675.111 1.422-.364 1.891l-.814.806c-.049.048-.098.147-.088.294.016.257.016.515 0 .772-.01.147.038.246.088.294l.814.806c.475.469.679 1.216.364 1.891a7.977 7.977 0 0 1-.704 1.217c-.428.61-1.176.807-1.82.63l-1.102-.302c-.067-.019-.176-.011-.299.071a4.909 4.909 0 0 1-.668.386c-.133.066-.194.156-.212.223l-.288 1.108c-.17.644-.716 1.195-1.36 1.259a8.34 8.34 0 0 1-1.401 0c-.645-.064-1.191-.615-1.361-1.26l-.288-1.107c-.018-.067-.079-.158-.212-.224a4.95 4.95 0 0 1-.668-.386c-.123-.082-.233-.09-.299-.071l-1.103.303c-.644.176-1.392-.021-1.82-.63a8.12 8.12 0 0 1-.704-1.218c-.315-.675-.111-1.422.363-1.891l.815-.806c.05-.048.098-.147.088-.294a6.214 6.214 0 0 1 0-.772c.01-.147-.038-.246-.088-.294l-.815-.806C.635 6.045.431 5.298.746 4.623a7.92 7.92 0 0 1 .704-1.217c.428-.61 1.176-.807 1.82-.63l1.102.302c.067.019.176.011.299-.071.214-.143.437-.272.668-.386.133-.066.194-.156.212-.223l.288-1.108c.17-.644.716-1.195 1.361-1.259A8.29 8.29 0 0 1 8 0Zm0 5a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z"/>`,
		"org":    `<path d="M1.75 16A1.75 1.75 0 0 1 0 14.25V1.75C0 .784.784 0 1.75 0h8.5C11.216 0 12 .784 12 1.75v12.5c0 .085-.006.168-.018.25h2.268a.25.25 0 0 0 .25-.25V8.285a.25.25 0 0 0-.111-.208l-1.055-.703a.749.749 0 1 1 .832-1.248l1.055.703c.487.325.779.871.779 1.456v5.965A1.75 1.75 0 0 1 14.25 16h-3.5a.766.766 0 0 1-.197-.026c-.099.017-.2.026-.303.026h-.5a.75.75 0 0 1-.75-.75v-3.5a.25.25 0 0 0-.25-.25h-1.5a.25.25 0 0 0-.25.25v3.5a.75.75 0 0 1-.75.75h-.5a.75.75 0 0 1-.75-.75v-3.5a.25.25 0 0 0-.25-.25h-1.5a.25.25 0 0 0-.25.25v3.5a.75.75 0 0 1-.75.75h-.5A1.75 1.75 0 0 1 0 14.25v-9Zm1.5-9v9c0 .085.006.168.018.25h2.732a.25.25 0 0 0 .25-.25v-3.5c0-.69.56-1.25 1.25-1.25h1.5c.69 0 1.25.56 1.25 1.25v3.5c0 .085.006.168.018.25h.232a.25.25 0 0 0 .25-.25V1.75a.25.25 0 0 0-.25-.25h-8.5a.25.25 0 0 0-.25.25Z"/>`,
		"code":   `<path d="M4.72 3.22a.75.75 0 0 1 1.06 1.06L2.06 8l3.72 3.72a.749.749 0 0 1-.326 1.275.749.749 0 0 1-.734-.215L.47 8.53a.75.75 0 0 1 0-1.06Zm6.56 0a.75.75 0 1 0-1.06 1.06L13.94 8l-3.72 3.72a.749.749 0 0 0 .326 1.275.749.749 0 0 0 .734-.215l3.229-3.25a.75.75 0 0 0 0-1.06Z"/>`,
	}
	return func(name string) template.HTML {
		d, ok := paths[name]
		if !ok {
			return ""
		}
		return template.HTML(`<svg class="octicon" viewBox="0 0 16 16" width="` +
			itoa(size) + `" height="` + itoa(size) + `" fill="currentColor" aria-hidden="true">` + d + `</svg>`)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---- shared page chrome ----

type base struct {
	Title  string
	User   string
	Admin  bool
	Tester bool // 测试角色：导航与页面隐藏源码/流水线入口
	Msg    string
	Host   string
}

const faviconURI = `data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Crect width='16' height='16' rx='3.5' fill='%23238636'/%3E%3Cpath d='M4.6 5.4 2.8 8l1.8 2.6M11.4 5.4 13.2 8l-1.8 2.6' stroke='white' stroke-width='1.5' fill='none' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E`

const pageCSS = `
:root{
--bg:#0d1117;--bg2:#161b22;--bg3:#21262d;--bg-inset:#010409;
--border:#30363d;--border2:#21262d;
--fg:#e6edf3;--muted:#8b949e;--accent:#58a6ff;
--green:#3fb950;--green-btn:#238636;--green-btn-h:#2ea043;
--red:#f85149;--yellow:#d29922;--radius:6px;
--mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,"Liberation Mono",monospace}
*{box-sizing:border-box}
html{color-scheme:dark}
body{margin:0;background:var(--bg);color:var(--fg);
font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans SC",Helvetica,Arial,sans-serif;
-webkit-font-smoothing:antialiased}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
code{font-family:var(--mono);font-size:12px;background:rgba(110,118,129,.4);padding:1px 7px;border-radius:6px}
p{margin:6px 0;line-height:1.6}
h1{font-size:20px;margin:8px 0}h2{font-size:14px;color:var(--fg);margin:16px 0 8px}
.muted{color:var(--muted);font-size:13px}
.sep{color:var(--muted)}
main{max-width:1240px;margin:0 auto;padding:24px 16px}

/* nav */
nav{position:sticky;top:0;z-index:20;background:var(--bg2);border-bottom:1px solid var(--border)}
.nav-inner{max-width:1240px;margin:0 auto;padding:0 16px;height:56px;display:flex;align-items:center;gap:6px}
.brand{display:flex;align-items:center;gap:9px;font-weight:600;color:var(--fg);font-size:15px;margin-right:12px}
.brand svg{color:var(--accent)}
.brand:hover{text-decoration:none}
nav a.nlink{color:#c9d1d9;padding:6px 12px;border-radius:6px;font-size:14px}
nav a.nlink:hover{background:var(--bg3);color:#fff;text-decoration:none}
nav .spacer{flex:1}
.uchip{display:flex;align-items:center;gap:8px;color:#c9d1d9;font-size:13px}
.uchip:hover{text-decoration:none;color:#fff}
.avatar{display:inline-flex;align-items:center;justify-content:center;flex:none;
width:22px;height:22px;border-radius:50%;color:#fff;font-size:11px;font-weight:600}
.avatar-lg{width:26px;height:26px;font-size:12px}

/* buttons */
button,.btn{display:inline-flex;align-items:center;justify-content:center;gap:7px;
padding:5px 14px;font-size:13px;font-weight:500;line-height:20px;color:var(--fg);
background:var(--bg3);border:1px solid rgba(240,246,252,.12);border-radius:var(--radius);cursor:pointer;
font-family:inherit}
button:hover,.btn:hover{background:#30363d;text-decoration:none}
button svg{flex:none}
.btn-primary{background:var(--green-btn);border-color:rgba(240,246,252,.12);color:#fff}
.btn-primary:hover{background:var(--green-btn-h)}
.ghost{background:transparent}
.ghost:hover{background:var(--bg3)}
.danger{background:transparent;border-color:rgba(248,81,73,.45);color:var(--red)}
.danger:hover{background:rgba(248,81,73,.14)}
.btn-sm{padding:3px 10px;font-size:12px;line-height:18px}
form.inline{display:inline}

/* pills & badges */
.pill,.badge{display:inline-block;padding:0 8px;font-size:12px;line-height:20px;
border-radius:9999px;border:1px solid var(--border);color:var(--muted);white-space:nowrap}
.pill.public{color:var(--green);border-color:rgba(63,185,80,.5);background:rgba(63,185,80,.1)}
.pill.private{color:var(--muted)}
.badge.success{color:var(--green);border-color:rgba(63,185,80,.5);background:rgba(63,185,80,.1)}
.badge.failed{color:var(--red);border-color:rgba(248,81,73,.5);background:rgba(248,81,73,.1)}
.badge.running{color:var(--yellow);border-color:rgba(210,153,34,.5);background:rgba(210,153,34,.1)}
.badge.pending{color:var(--muted);border-color:var(--border)}
.badge.skipped{color:var(--yellow);border-color:rgba(210,153,34,.5);background:rgba(210,153,34,.1)}

/* flash / error */
.msg,.err{padding:10px 14px;border-radius:var(--radius);margin:14px 0;font-size:13px}
.msg{background:rgba(63,185,80,.1);border:1px solid rgba(63,185,80,.45);color:#7ee787}
.err{background:rgba(248,81,73,.1);border:1px solid rgba(248,81,73,.45);color:#ffc4bf}

/* boxes & cards */
.box{border:1px solid var(--border);border-radius:var(--radius);margin:16px 0;overflow:hidden}
.box-header{padding:10px 16px;background:var(--bg2);border-bottom:1px solid var(--border);
font-size:13px;font-weight:600;color:var(--muted);display:flex;align-items:center;gap:8px}
.box-header .spacer{flex:1}
.box-row{padding:10px 16px}
.box-row + .box-row{border-top:1px solid var(--border2)}
.card{background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);padding:16px 20px;margin:16px 0}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:16px}
.empty{padding:28px 16px;text-align:center;color:var(--muted);font-size:13px}

/* tables */
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;color:var(--muted);font-weight:500;font-size:12px;padding:8px 12px;border-bottom:1px solid var(--border)}
td{padding:8px 12px;border-bottom:1px solid var(--border2);vertical-align:middle}
tbody tr:hover td{background:rgba(110,118,129,.06)}

/* forms */
label{display:block;color:var(--muted);font-size:12px;margin:12px 0 4px}
input,select{background:var(--bg-inset);border:1px solid var(--border);color:var(--fg);
border-radius:var(--radius);padding:6px 12px;font-size:13px;font-family:inherit;width:100%;max-width:400px}
input:focus,select:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px rgba(88,166,255,.3)}
form.inline input{width:150px}
select{width:auto;min-width:160px}

/* repo list */
.repo-item{display:flex;align-items:center;gap:12px;padding:12px 16px}
.repo-item + .repo-item{border-top:1px solid var(--border2)}
.repo-item:hover{background:rgba(110,118,129,.05)}
.repo-item .rname{display:flex;align-items:center;gap:8px;font-size:14px}
.repo-item .rname>svg{color:var(--muted)}
.repo-item .rname a{color:var(--accent);font-weight:600;font-size:15px}
.repo-item .meta{color:var(--muted);font-size:12px;margin-top:3px;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.repo-item .meta svg{vertical-align:-2px}
.repo-item .right{margin-left:auto;text-align:right;font-size:12px;color:var(--muted);flex:none}

/* repo header */
.repo-head{background:var(--bg2);border-bottom:1px solid var(--border);padding:16px 0 0}
.repo-head .inner{max-width:1240px;margin:0 auto;padding:0 16px}
.title-row{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.title-row h1{font-size:18px;font-weight:400;margin:0;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.title-row h1>svg{color:var(--muted)}
.title-row h1 b{font-weight:600}
.title-row .actions{margin-left:auto;display:flex;gap:8px;flex-wrap:wrap}
.tabs{display:flex;gap:2px;margin-top:14px;flex-wrap:wrap}
.tabs a{display:flex;align-items:center;gap:8px;padding:8px 14px;color:#c9d1d9;font-size:14px;
border-bottom:2px solid transparent;border-radius:6px 6px 0 0}
.tabs a svg{color:var(--muted)}
.tabs a:hover{color:#fff;background:var(--bg3);text-decoration:none}
.tabs a.active{font-weight:600;border-bottom-color:#f78166}
.tabs a.active svg{color:var(--fg)}

/* layout */
.layout{display:grid;grid-template-columns:minmax(0,1fr) 320px;gap:24px;align-items:start}
@media(max-width:900px){.layout{grid-template-columns:1fr}}

/* breadcrumbs & file list */
.crumbs{font-size:16px;display:flex;align-items:center;gap:4px;flex-wrap:wrap;margin:4px 0 2px}
.crumbs .final{font-weight:600}
.file-row{display:flex;align-items:center;gap:10px;padding:8px 16px;font-size:13px}
.file-row + .file-row{border-top:1px solid var(--border2)}
.file-row:hover{background:rgba(110,118,129,.06)}
.file-row>svg{color:var(--muted);flex:none}
.file-row.dir>svg{color:#54aeff}
.file-row .fname{color:var(--fg)}
.file-row:hover .fname{color:var(--accent)}
.file-row .fsize{margin-left:auto;color:var(--muted);font-size:12px}

/* blob */
.blob-head{display:flex;align-items:center;gap:12px;padding:10px 16px;background:var(--bg2);
border:1px solid var(--border);border-radius:var(--radius) var(--radius) 0 0;font-size:13px;flex-wrap:wrap}
.blob-head .right{margin-left:auto;display:flex;gap:8px;align-items:center}
.blob-body{border:1px solid var(--border);border-top:0;border-radius:0 0 var(--radius) var(--radius);overflow:hidden}
pre{margin:0;padding:14px 16px;overflow:auto;background:var(--bg-inset);color:var(--fg);
font:12px/1.6 var(--mono);max-height:70vh;white-space:pre-wrap;word-break:break-word}
/* markdown 文档（缺陷单/评论） */
.md{line-height:1.75;font-size:13.5px;overflow-wrap:break-word}
.md h1,.md h2,.md h3{font-size:14px;margin:14px 0 6px;color:var(--fg);border-bottom:1px solid var(--border2);padding-bottom:4px}
.md p{margin:6px 0}
.md ul,.md ol{margin:6px 0;padding-left:22px}
.md li{margin:3px 0}
.md img{max-width:100%;max-height:420px;border:1px solid var(--border);border-radius:6px;margin:4px 0}
.md blockquote{margin:8px 0;padding:2px 12px;border-left:3px solid var(--border);color:var(--muted)}
.md table{border:1px solid var(--border)}
.md table td,.md table th{padding:4px 10px}
/* 缺陷单 */
.bug-head{display:flex;align-items:flex-start;gap:10px;flex-wrap:wrap;padding:14px 16px}
.bug-head .t{font-size:16px;font-weight:600}
.bug-meta{display:flex;gap:10px;align-items:center;flex-wrap:wrap;font-size:12px;color:var(--muted)}
.shots{display:flex;gap:10px;flex-wrap:wrap;padding:10px 16px}
.shots img{max-width:220px;max-height:160px;border:1px solid var(--border);border-radius:6px}
textarea{background:var(--bg-inset);border:1px solid var(--border);color:var(--fg);border-radius:var(--radius);padding:8px 12px;font-size:13px;font-family:inherit;width:100%;min-height:72px;resize:vertical}
textarea:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px rgba(88,166,255,.3)}

/* 代码查看（blobJS 生成的行号表格；无 JS 时回退为上面的 pre） */
table.code{width:100%;border-collapse:collapse;font:12px/1.6 var(--mono);background:var(--bg-inset);
display:block;overflow:auto;max-height:70vh}
table.code td{padding:0;border:0;vertical-align:top}
table.code tr:hover td{background:rgba(110,118,129,.07)}
table.code td.ln{width:1%;min-width:44px;text-align:right;padding:0 10px;color:var(--muted);
user-select:none;opacity:.65;position:sticky;left:0;background:var(--bg-inset)}
table.code td.lc{padding:0 14px;white-space:pre;word-break:normal;color:var(--fg)}
.tok-k{color:#ff7b72}.tok-s{color:#a5d6ff}.tok-c{color:#8b949e;font-style:italic}
.tok-n{color:#f2cc60}.tok-p{color:#7ee787}.tok-t{color:#79c0ff}

/* commits */
.commit{display:flex;gap:12px;padding:10px 16px;align-items:flex-start}
.commit + .commit{border-top:1px solid var(--border2)}
.commit:hover{background:rgba(110,118,129,.05)}
.commit .cmsg{font-weight:600;color:var(--fg)}
.commit:hover .cmsg{color:var(--accent)}
.commit .cmeta{color:var(--muted);font-size:12px;display:flex;gap:8px;align-items:center;margin-top:2px;flex-wrap:wrap}
.commit .csha{margin-left:auto;flex:none;align-self:center}
.commit .csha a{color:var(--muted)}
.commit .csha a:hover{color:var(--accent)}

/* pipelines */
.pl-row{display:flex;align-items:center;gap:12px;padding:10px 16px}
.pl-row + .pl-row{border-top:1px solid var(--border2)}
.pl-row:hover{background:rgba(110,118,129,.05)}
.pl-main{min-width:0}
.status-icon{width:30px;height:30px;border-radius:50%;display:flex;align-items:center;justify-content:center;flex:none}
.status-icon svg{width:16px;height:16px}
.status-icon.success{color:var(--green);background:rgba(63,185,80,.15)}
.status-icon.failed{color:var(--red);background:rgba(248,81,73,.15)}
.status-icon.running{color:var(--yellow);background:rgba(210,153,34,.15)}
.status-icon.pending{color:var(--muted);background:rgba(139,148,158,.15)}
.status-icon.skipped{color:var(--yellow);background:rgba(210,153,34,.15)}
.pl-row .t{font-size:14px;font-weight:600}
.pl-row .t a{color:var(--fg)}
.pl-row .t a:hover{color:var(--accent)}
.pl-row .m{color:var(--muted);font-size:12px;display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:2px}
.pl-row .right{margin-left:auto;text-align:right;color:var(--muted);font-size:12px;flex:none}

/* steps timeline */
.steps{margin:8px 0}
.step{display:flex;gap:14px;align-items:flex-start;padding:8px 0}
.step .status-icon{width:26px;height:26px}
.step .sname{font-weight:600;font-size:13px;padding-top:3px}
.step .sdur{color:var(--muted);font-size:12px;padding-top:5px}
.step .sdetail{color:var(--red);font-size:12px;padding-top:5px;word-break:break-word}

/* login */
.login-wrap{min-height:calc(100vh - 120px);display:flex;flex-direction:column;align-items:center;justify-content:center;padding:32px 16px}
.login-logo{display:flex;align-items:center;gap:10px;font-size:22px;font-weight:600;margin-bottom:4px}
.login-logo svg{color:var(--green)}
.login-card{width:100%;max-width:340px;border:1px solid var(--border);border-radius:var(--radius);padding:20px;background:var(--bg2)}
`

// blobJS is the self-contained browser-side syntax highlighter for the blob
// view (~150 lines, no external assets, works on intranet/offline servers).
// It reads #code (textContent + data-lang), tokenizes per line with carried
// state (block comments / strings), and replaces the <pre> with a line-number
// table. Without JS the plain-text pre stays — progressive enhancement.
const blobJS = `(function(){
"use strict";
var pre = document.getElementById('code');
if (!pre) return;
var text = pre.textContent.replace(/\r\n/g, '\n');
if (!text) return;
if (text.charAt(text.length - 1) === '\n') text = text.slice(0, -1);
var lang = (pre.getAttribute('data-lang') || '').toLowerCase();

function kwset(s) {
  var o = {};
  if (s) { var ws = s.split(/\s+/); for (var i = 0; i < ws.length; i++) o[ws[i]] = 1; }
  return o;
}

var KW = {
  go: 'break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false make new len cap append copy delete panic recover print println close error any string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr float32 float64 bool byte rune complex64 complex128',
  js: 'break case catch class const continue debugger default delete do else export extends finally for function if import in instanceof let new return static super switch this throw try typeof var void while with yield async await of null true false undefined NaN Infinity',
  ts: 'break case catch class const continue debugger default delete do else export extends finally for function if import in instanceof let new return static super switch this throw try typeof var void while with yield async await of null true false undefined interface type enum implements readonly namespace declare abstract public private protected keyof as satisfies',
  py: 'and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return True False None while with yield match case self print len range str int float list dict set tuple bool bytes open',
  java: 'abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while var record sealed null true false String Integer Boolean Long Double List Map',
  c: 'auto break case char const continue default do double else enum extern float for goto if inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void volatile while NULL true false size_t ssize_t int8_t int16_t int32_t int64_t uint8_t uint16_t uint32_t uint64_t',
  cpp: 'auto break case char const constexpr continue default do double else enum explicit extern float for goto if inline int long mutable namespace new operator private protected public register return short signed sizeof static struct switch template this throw try typedef typename union unsigned using virtual void volatile while class std nullptr true false bool catch override final',
  rust: 'as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while true i8 i16 i32 i64 i128 u8 u16 u32 u64 u128 usize isize f32 f64 bool char str String Vec Option Result Some None Ok Err Box',
  sh: 'if then else elif fi for while until do done case esac function in return exit local export readonly set unset shift echo cd ls pwd break continue source trap eval exec declare printf read test',
  rb: 'def end class module if elsif else unless while until for in do then begin rescue ensure yield return self nil true false require require_relative puts attr_accessor attr_reader new lambda proc raise',
  php: 'function return echo print class public private protected extends implements interface trait namespace use new static self parent if else elseif while for foreach as switch case break continue try catch finally throw null true false require include',
  sql: 'select from where insert into values update set delete create table drop alter add index view join left right inner outer full on group by order having limit offset as and or not null primary key foreign references unique default distinct union all exists between like in is asc desc begin commit rollback'
};

var BT = String.fromCharCode(96); /* 反引号：Go raw string 字面量里不能出现 */
var CFG = {
  go:   { kw: kwset(KW.go), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'", BT] },
  js:   { kw: kwset(KW.js), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'", BT] },
  ts:   { kw: kwset(KW.ts), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'", BT] },
  py:   { kw: kwset(KW.py), lc: ['#'], bc: [['"""', '"""'], ["'''", "'''"]], st: ['"', "'"] },
  java: { kw: kwset(KW.java), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'"] },
  c:    { kw: kwset(KW.c), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'"] },
  cpp:  { kw: kwset(KW.cpp), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'"] },
  rust: { kw: kwset(KW.rust), lc: ['//'], bc: [['/*', '*/']], st: ['"', "'"] },
  sh:   { kw: kwset(KW.sh), lc: ['#'], bc: [], st: ['"', "'"] },
  rb:   { kw: kwset(KW.rb), lc: ['#'], bc: [['=begin', '=end']], st: ['"', "'"] },
  php:  { kw: kwset(KW.php), lc: ['//', '#'], bc: [['/*', '*/']], st: ['"', "'"] },
  sql:  { kw: kwset(KW.sql), lc: ['--'], bc: [['/*', '*/']], st: ["'", '"'] },
  yaml: { kw: kwset(''), lc: ['#'], bc: [], st: ['"', "'"], prop: true },
  json: { kw: kwset(''), lc: [], bc: [], st: ['"'] },
  toml: { kw: kwset(''), lc: ['#'], bc: [], st: ['"', "'"], prop: true },
  ini:  { kw: kwset(''), lc: [';', '#'], bc: [], st: ['"', "'"], prop: true },
  css:  { kw: kwset(''), lc: [], bc: [['/*', '*/']], st: ['"', "'"], prop: true },
  html: { kw: kwset(''), lc: [], bc: [['<!--', '-->']], st: ['"', "'"], tag: true },
  xml:  { kw: kwset(''), lc: [], bc: [['<!--', '-->']], st: ['"', "'"], tag: true }
};

var cfg = CFG[lang] || { kw: kwset(''), lc: ['#', '//'], bc: [['/*', '*/']], st: ['"', "'"] };
var wordRE = /[A-Za-z0-9_$]/;
var wordStartRE = /^[A-Za-z0-9_$]+/;

// tokenize: 按行分词，块注释与字符串状态跨行延续。
// 返回 [行][token(class, text)]，class 为 ''(普通)/k(关键字)/s(字符串)/c(注释)/n(数字)/p(属性)。
function tokenize(t) {
  var lines = t.split('\n'), out = [];
  var state = null; // null | {t:'bc', end:'*/'} | {t:'st', q:'"'}
  for (var li = 0; li < lines.length; li++) {
    var line = lines[li], toks = [], buf = '', i = 0;
    function flush() { if (buf) { toks.push(['', buf]); buf = ''; } }
    while (i < line.length) {
      if (state && state.t === 'bc') {
        var eidx = line.indexOf(state.end, i);
        if (eidx === -1) { toks.push(['c', line.slice(i)]); i = line.length; }
        else { toks.push(['c', line.slice(i, eidx + state.end.length)]); i = eidx + state.end.length; state = null; }
        continue;
      }
      if (state && state.t === 'st') {
        var q = state.q, j = i, found = -1;
        while (j < line.length) {
          if (line.charAt(j) === '\\' && (q === '"' || q === "'")) { j += 2; continue; }
          if (line.charAt(j) === q) { found = j; break; }
          j++;
        }
        if (found === -1) { toks.push(['s', line.slice(i)]); i = line.length; }
        else { toks.push(['s', line.slice(i, found + 1)]); i = found + 1; state = null; }
        continue;
      }
      var lc = cfg.lc, hit = false, k;
      for (k = 0; k < lc.length; k++) {
        if (line.startsWith(lc[k], i)) { flush(); toks.push(['c', line.slice(i)]); i = line.length; hit = true; break; }
      }
      if (hit) continue;
      for (k = 0; k < cfg.bc.length; k++) {
        if (line.startsWith(cfg.bc[k][0], i)) {
          flush();
          toks.push(['c', cfg.bc[k][0]]); // 开标记归属本行
          state = { t: 'bc', end: cfg.bc[k][1] };
          i += cfg.bc[k][0].length;
          hit = true; break;
        }
      }
      if (hit) continue;
      for (k = 0; k < cfg.st.length; k++) {
        if (line.charAt(i) === cfg.st[k]) {
          flush();
          toks.push(['s', cfg.st[k]]); // 开引号归属本行
          state = { t: 'st', q: cfg.st[k] };
          i++;
          hit = true; break;
        }
      }
      if (hit) continue;
      var ch = line.charAt(i);
      if (cfg.tag && ch === '<') {
        var m = line.slice(i).match(/^<\/?[A-Za-z][A-Za-z0-9:_-]*/);
        if (m) { flush(); toks.push(['k', m[0]]); i += m[0].length; continue; }
      }
      if (wordRE.test(ch)) {
        var w = line.slice(i).match(wordStartRE)[0];
        var cls = '';
        if (cfg.kw[w]) cls = 'k';
        else if (ch >= '0' && ch <= '9') cls = 'n';
        else if (cfg.prop && line.charAt(i + w.length) === ':') cls = 'p';
        flush();
        toks.push([cls, w]);
        i += w.length;
        continue;
      }
      buf += ch; i++;
    }
    flush();
    out.push(toks);
  }
  return out;
}

var toks = tokenize(text);
var tbl = document.createElement('table');
tbl.className = 'code';
for (var li2 = 0; li2 < toks.length; li2++) {
  var tr = document.createElement('tr');
  var ln = document.createElement('td');
  ln.className = 'ln';
  ln.textContent = li2 + 1;
  var lc2 = document.createElement('td');
  lc2.className = 'lc';
  var ts = toks[li2];
  for (var ti = 0; ti < ts.length; ti++) {
    var sp = document.createElement('span');
    if (ts[ti][0]) sp.className = 'tok-' + ts[ti][0];
    sp.textContent = ts[ti][1];
    lc2.appendChild(sp);
  }
  if (!ts.length) lc2.appendChild(document.createTextNode(' '));
  tr.appendChild(ln);
  tr.appendChild(lc2);
  tbl.appendChild(tr);
}
pre.replaceWith(tbl);
})();`

var pages = template.Must(template.New("root").Funcs(tmplFuncs).Parse(`
{{define "head"}}<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<link rel="icon" href="` + faviconURI + `">
<title>{{.Title}} · Gitwo</title><style>` + pageCSS + `</style></head><body>{{end}}

{{define "nav"}}<nav><div class="nav-inner">
<a class="brand" href="/">{{oct "repo"}}<span>Gitwo</span></a>
{{if .User}}<a class="nlink" href="/">仓库</a>
{{if not .Tester}}<a class="nlink" href="/pipelines">流水线</a>{{end}}
{{if .Admin}}<a class="nlink" href="/admin">管理</a>{{end}}
<span class="spacer"></span>
<a class="uchip" href="/me" title="账户设置"><span class="avatar" style="background:{{avacolor .User}}">{{initials .User}}</span>{{.User}}</a>
<form class="inline" method="post" action="/logout"><button class="ghost btn-sm">退出</button></form>
{{else}}<span class="spacer"></span><a class="btn btn-primary btn-sm" href="/login">登录</a>{{end}}
</div></nav>{{end}}

{{define "flash"}}{{if .Msg}}<div class="msg">{{.Msg}}</div>{{end}}{{end}}

{{define "foot"}}</main></body></html>{{end}}

{{define "statusicon"}}<span class="status-icon {{.}}">{{if eq . "success"}}{{oct "check"}}{{else if eq . "failed"}}{{oct "x"}}{{else if eq . "skipped"}}{{oct "x"}}{{else}}{{oct "dot"}}{{end}}</span>{{end}}

{{define "plrow"}}
<div class="pl-row">
{{template "statusicon" .Status}}
<div class="pl-main">
<div class="t"><a href="/pipelines/{{.ID}}">{{.Repo}} <span class="sep">/</span> {{.Branch}}</a></div>
<div class="m"><span>{{oct "commit"}} <code>{{shal .SHA}}</code></span>
{{if .Error}}<span class="badge failed">{{.Error}}</span>{{end}}</div>
</div>
<div class="right">{{fdur .Duration}}<br>{{ftime .CreatedAt}}</div>
</div>
{{end}}

{{define "login"}}
{{template "head" .Base}}<main>
<div class="login-wrap">
<div class="login-logo">{{octBig "repo"}} Gitwo</div>
<p class="muted">自包含 Git 服务端 · 流水线 · 站点托管</p>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="login-card">
<form method="post" action="/login">
<label>用户名</label><input name="username" value="{{.Username}}" autofocus>
<label>密码</label><input name="password" type="password">
<p><button class="btn-primary" style="width:100%">登录</button></p>
</form>
</div>
<p class="muted">首次启动默认账户 admin / admin，请登录后立即在右上角头像处修改。</p>
</div>
</main></body></html>{{end}}

{{define "me"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<h1>账户设置</h1>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="grid">
<div class="card">
<h2>修改密码</h2>
<form method="post" action="/me">
<label>当前密码</label><input name="current" type="password">
<label>新密码</label><input name="password" type="password">
<label>确认新密码</label><input name="confirm" type="password">
<p><button class="btn-primary">更新密码</button></p>
</form>
</div>
<div class="card">
<h2>账户信息</h2>
<p><span class="avatar avatar-lg" style="background:{{avacolor .User.Name}}">{{initials .User.Name}}</span>
<b>{{.User.Name}}</b> {{if .User.Admin}}<span class="badge success">管理员</span>{{else if eq .User.Role "tester"}}<span class="badge skipped">测试</span>{{else}}<span class="badge">开发者</span>{{end}}</p>
<p class="muted">所在组织：{{if .Orgs}}{{range .Orgs}}<code>{{.}}</code> {{end}}{{else}}无{{end}}</p>
<p><a href="/me/keys">SSH 公钥管理 →</a> <span class="muted">添加公钥后可用 SSH 推拉仓库</span></p>
</div>
</div>
</main>{{template "foot"}}{{end}}

{{define "me_keys"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<h1>SSH 公钥</h1>
<p><a href="/me">← 返回账户设置</a></p>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card" style="max-width:860px">
<h2>添加公钥</h2>
<form method="post" action="/me/keys/add">
<label>公钥内容（authorized_keys 单行格式）*</label>
<textarea name="pub" required placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... user@host" style="font-family:var(--mono);min-height:88px"></textarea>
<label>名称（选填，缺省取公钥备注）</label><input name="name" placeholder="笔记本 / CI">
<p style="margin-top:12px"><button class="btn-primary">添加公钥</button></p>
<p class="muted">支持 ssh-ed25519 / ssh-rsa / ecdsa；添加后即可使用 ssh://git@&lt;host&gt;:&lt;SSH端口&gt;/&lt;owner&gt;/&lt;repo&gt;.git 推拉仓库。一把公钥只能绑定一个账户。</p>
</form>
</div>
<div class="box">
<div class="box-header">已保存的公钥（{{len .Keys}}）</div>
{{range .Keys}}
<div class="box-row" style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
<b>{{.Name}}</b>
<span class="pill">{{.Type}}</span>
<code style="word-break:break-all">{{.Fingerprint}}</code>
<span class="muted">{{ftime .AddedAt}}</span>
<form class="inline" method="post" action="/me/keys/{{.ID}}/delete" onsubmit="return confirm('删除公钥 {{.Name}}？')">
<button class="danger btn-sm">删除</button></form>
</div>
{{else}}
<div class="empty">还没有保存任何公钥。添加公钥后即可通过 SSH 进行 git clone / push。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "dash"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<div class="title-row" style="margin-bottom:4px">
<h1>{{if .Tester}}测试项目{{else if .LoggedIn}}我的仓库{{else}}公开仓库{{end}}</h1>
<span style="flex:1"></span>
{{if and .LoggedIn (not .Tester)}}<input id="repoq" placeholder="搜索仓库…" style="max-width:220px;margin-right:8px" oninput="filterRepos(this.value)">
<a href="/repos/new" class="btn btn-primary">{{oct "repo"}} 新建仓库</a>{{end}}
</div>
{{if .LoggedIn}}
{{$full := ""}}
<div class="box" style="border-color:rgba(88,166,255,.35)">
<div class="box-header">{{oct "play"}} 我的待办</div>
{{range .TodoBugsFix}}
<div class="pl-row">
{{template "statusicon" (bugStatusClass .Status)}}
<div class="pl-main">
<div class="t"><span class="badge failed">待修复</span> <a href="/repos/{{.Repo}}/bugs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>缺陷 · {{.Repo}}</span><span>提交人 <code>{{.Reporter}}</code></span></div>
</div>
</div>
{{end}}
{{range .TodoBugsTest}}
<div class="pl-row">
{{template "statusicon" (bugStatusClass .Status)}}
<div class="pl-main">
<div class="t"><span class="badge running">待复测</span> <a href="/repos/{{.Repo}}/bugs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>缺陷 · {{.Repo}}</span><span>指派给 <code>{{.Assignee}}</code></span></div>
</div>
</div>
{{end}}
{{range .TodoReqsCheck}}
<div class="pl-row">
{{template "statusicon" (reqStatusClass .Status)}}
<div class="pl-main">
<div class="t"><span class="badge running">待核查</span> <a href="/repos/{{.Repo}}/reqs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>需求 · {{.Repo}}</span></div>
</div>
</div>
{{end}}
{{if not (or .TodoBugsFix (or .TodoBugsTest .TodoReqsCheck))}}
<div class="empty">没有待办事项 ✓</div>
{{end}}
</div>
{{end}}
{{template "flash" .Base}}
<div class="box">
<div class="box-header">共 {{len .Repos}} 个仓库{{if not .Tester}} <span class="spacer"></span><a href="/pipelines">流水线 →</a>{{end}}</div>
{{$logged := .LoggedIn}}
{{$tester := .Tester}}
{{range .Repos}}
<div class="repo-item" data-name="{{.FullName}} {{ownerLabel .Owner}}">
<div>
<div class="rname">{{oct "repo"}} <a href="/repos/{{.FullName}}">{{.FullName}}</a>
{{if .Public}}<span class="pill public">Public</span>{{else}}<span class="pill private">Private</span>{{end}}</div>
<div class="meta">
<span>{{oct "org"}} {{ownerLabel .Owner}}</span>
{{if not $tester}}{{if .DeployBranch}}<span>{{oct "code"}} <code>{{.DeployBranch}}</code></span>
{{else}}<span>任意含 deploy.yaml 的分支</span>{{end}}{{end}}
</div>
</div>
<div class="right">
{{if .LastTask}}
<div style="display:flex;align-items:center;gap:6px;justify-content:flex-end">
{{template "statusicon" .LastTask.Status}}
<span>{{.LastTask.Status}} · {{ftime .LastTask.CreatedAt}}</span>
</div>
{{if not $tester}}<a href="/pipelines?repo={{.FullName}}">查看流水线</a>{{end}}
{{else}}从未部署{{end}}
{{if .HasSite}}<div><a href="{{.SiteURL}}">{{oct "play"}} 访问站点</a></div>{{end}}
</div>
</div>
{{else}}
<div class="empty">
{{if $tester}}还没有分配给你的测试项目。请联系管理员把相关仓库归属到你所在的团队。
{{else if $logged}}还没有仓库。点击右上角「新建仓库」创建第一个仓库，push 一个包含 deploy.yaml 的提交即可自动部署。
{{else}}登录后可创建仓库；当前展示的是公开仓库。{{end}}
</div>
{{end}}
</div>
<script>
function filterRepos(q){q=q.toLowerCase();document.querySelectorAll('.repo-item').forEach(function(el){
  el.style.display=(el.dataset.name||'').toLowerCase().indexOf(q)>=0?'':'none';});}
</script>
</main>{{template "foot"}}{{end}}

{{define "repo_new"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<h1>新建仓库</h1>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card" style="max-width:560px">
<form method="post" action="/repos/new">
<label>仓库名称（字母数字_-）</label><input name="name" value="{{.Name}}" required>
<label>归属</label>
<select name="owner">{{range .Owners}}<option value="{{.Value}}">{{.Label}}</option>{{end}}</select>
<label>可见性</label>
<select name="visibility">
<option value="private">Private — 仅归属者与组织成员</option>
<option value="public">Public — 匿名可只读</option>
</select>
<label>部署分支（留空 = 任何含 deploy.yaml 的分支都自动部署）</label>
<input name="deploy_branch" placeholder="例如 main">
<p style="margin-top:16px"><button class="btn-primary">创建仓库</button></p>
</form>
<p class="muted">创建后获得 git 远程地址 http://&lt;host&gt;/git/&lt;归属名&gt;/&lt;仓库名&gt;.git（归属 = 用户名或组织名），推送包含 deploy.yaml 的提交即自动构建部署。</p>
</div>
</main>{{template "foot"}}{{end}}

{{define "repohead"}}
<div class="repo-head"><div class="inner">
<div class="title-row">
<h1>{{oct "repo"}} <a href="/">{{ownerLabel .Repo.Owner}}</a> <span class="sep">/</span> <b>{{.Repo.Name}}</b>
{{if .Repo.Public}}<span class="pill public">Public</span>{{else}}<span class="pill private">Private</span>{{end}}</h1>
<div class="actions">
{{if .CanWrite}}<form class="inline" method="post" action="/repos/{{.Repo.FullName}}/deploy"><button class="btn-primary">{{oct "play"}} 重新部署</button></form>{{end}}
{{if .CanManage}}<a href="/repos/{{.Repo.FullName}}/settings" class="btn ghost">{{oct "gear"}} 设置</a>{{end}}
{{if .HasSite}}<a href="/sites/{{.Repo.Slug}}/" class="btn">{{oct "play"}} 打开站点</a>{{end}}
</div>
</div>
<div class="tabs">
{{if not .QA}}
{{if .Head}}<a href="/repos/{{.Repo.FullName}}/tree/{{.Head}}" class="{{if eq .Tab "files"}}active{{end}}">{{oct "code"}} 代码</a>
<a href="/repos/{{.Repo.FullName}}" class="{{if eq .Tab "code"}}active{{end}}">{{oct "file"}} README</a>
{{else}}<a href="/repos/{{.Repo.FullName}}" class="{{if eq .Tab "code"}}active{{end}}">{{oct "code"}} 代码</a>{{end}}
<a href="/repos/{{.Repo.FullName}}/commits/{{.Head}}" class="{{if eq .Tab "commits"}}active{{end}}">{{oct "commit"}} 提交历史</a>
<a href="/repos/{{.Repo.FullName}}/logs" class="{{if eq .Tab "logs"}}active{{end}}">{{oct "file"}} 运行日志</a>
{{end}}
<a href="/repos/{{.Repo.FullName}}/reqs" class="{{if eq .Tab "reqs"}}active{{end}}">{{oct "file"}} 需求</a>
<a href="/repos/{{.Repo.FullName}}/bugs" class="{{if eq .Tab "bugs"}}active{{end}}">{{oct "x"}} 缺陷</a>
{{if .CanManage}}<a href="/repos/{{.Repo.FullName}}/settings" class="{{if eq .Tab "settings"}}active{{end}}">{{oct "gear"}} 设置</a>{{end}}
</div>
</div></div>
{{end}}

{{define "repo_overview"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
<div class="layout">
<div class="col-main">
{{if .README}}
<div class="box"><div class="box-header">{{oct "file"}} README.md</div>
<div class="box-row md" style="border-top:1px solid var(--border2)">{{.RenderedReadme}}</div></div>
{{else}}
<div class="box"><div class="empty">推送一个包含 README.md 的提交，它会显示在这里。</div></div>
{{end}}
<div class="box">
<div class="box-header">{{oct "file"}} 最近需求 <span class="spacer"></span><a href="/repos/{{$full}}/reqs">全部需求 →</a></div>
{{range .Reqs}}
<div class="pl-row">
{{template "statusicon" (reqStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/reqs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>创建者 <code>{{.Creator}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right"><span class="badge {{reqStatusClass .Status}}">{{reqStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有需求文档。</div>
{{end}}
</div>
<div class="box">
<div class="box-header">{{oct "x"}} 最近缺陷 <span class="spacer"></span><a href="/repos/{{$full}}/bugs">全部缺陷 →</a></div>
{{range .Bugs}}
<div class="pl-row">
{{template "statusicon" (bugStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/bugs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span class="badge {{severityClass .Severity}}">{{severityText .Severity}}</span>
<span>指派给 <code>{{.Assignee}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right"><span class="badge {{bugStatusClass .Status}}">{{bugStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有缺陷记录。</div>
{{end}}
</div>
<div class="box">
<div class="box-header">{{oct "play"}} 最近部署 <span class="spacer"></span><a href="/pipelines?repo={{.Repo.FullName}}">本仓库全部流水线 →</a></div>
{{range .Tasks}}
<div class="pl-row">
{{template "statusicon" .Status}}
<div class="pl-main">
<div class="t"><a href="/pipelines/{{.ID}}">{{.Branch}}</a></div>
<div class="m"><span>{{oct "commit"}} <code>{{shal .SHA}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right">
{{if $.CanWrite}}<form class="inline" method="post" action="/repos/{{$.Repo.FullName}}/deploy">
<input type="hidden" name="sha" value="{{.SHA}}">
<button class="ghost btn-sm" title="重新部署该历史提交（回滚）">回到此版本</button></form>{{end}}
{{fdur .Duration}}
</div>
</div>
{{else}}
<div class="empty">这个仓库还没有部署记录。push 或点击「重新部署」即可触发。</div>
{{end}}
</div>
</div>
<div class="col-side">
<div class="box">
<div class="box-header">关于</div>
<div class="box-row muted" style="display:flex;flex-direction:column;gap:8px">
<span>{{oct "org"}} 归属 {{ownerLabel .Repo.Owner}}</span>
<span>{{oct "clock"}} 创建于 {{ftime .Repo.CreatedAt}}</span>
<span>{{oct "code"}} 部署分支：{{if .Repo.DeployBranch}}<code>{{.Repo.DeployBranch}}</code>{{else}}任意含 deploy.yaml{{end}}</span>
{{if .ProtectedNote}}<span class="muted">{{.ProtectedNote}}</span>{{end}}
</div>
</div>
<div class="box">
<div class="box-header">Git 地址</div>
<div class="box-row"><code style="display:inline-block;padding:4px 10px;word-break:break-all">http://{{.Base.Host}}/git/{{.Repo.FullName}}.git</code>
<p class="muted" style="margin-top:8px">HTTPS：git clone http://&lt;用户&gt;:&lt;密码&gt;@{{.Base.Host}}/git/{{.Repo.FullName}}.git</p>
{{if .SSHURL}}<code style="display:inline-block;padding:4px 10px;word-break:break-all;margin-top:8px">{{.SSHURL}}</code>
<p class="muted" style="margin-top:8px">SSH：git clone {{.SSHURL}}（先在 <a href="/me/keys">账户设置</a> 添加公钥）</p>{{end}}
</div>
</div>
{{if .HasSite}}
<div class="box">
<div class="box-header">{{oct "play"}} 部署结果</div>
<div class="box-row" style="display:flex;flex-direction:column;gap:8px">
{{if .StaticDir}}<a href="/sites/{{.Repo.Slug}}/">{{oct "play"}} /sites/{{.Repo.Slug}}/</a>{{end}}
{{if .Port}}<a href="/api/{{.Repo.Slug}}/">/api/{{.Repo.Slug}}/（API 代理）</a>
<a href="http://{{.Base.Host}}:{{.Port}}/">http://{{.Base.Host}}:{{.Port}}/</a>
<span class="muted">（服务端口 {{.Port}}，经 9527 代理可不暴露）</span>{{end}}
</div>
</div>
{{end}}
<div class="box">
<div class="box-header">{{oct "commit"}} 分支</div>
{{range .Branches}}
<div class="box-row" style="display:flex;align-items:center;gap:8px">
{{oct "commit"}} <b><a href="/repos/{{$.Repo.FullName}}/tree/{{.Name}}">{{.Name}}</a></b>{{if eq .Name $.Head}}<span class="badge success">默认</span>{{end}}
<span style="flex:1"></span>
<code>{{shal .SHA}}</code>
</div>
{{end}}
</div>
</div>
</div>
</main>{{template "foot"}}{{end}}

{{define "repo_tester"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{template "flash" .Base}}
{{$full := .Repo.FullName}}
<div class="layout">
<div class="col-main">
<div class="box">
<div class="box-header">{{oct "file"}} 需求 <span class="spacer"></span><a href="/repos/{{$full}}/reqs">全部需求 →</a></div>
{{range .Reqs}}
<div class="pl-row">
{{template "statusicon" (reqStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/reqs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>创建者 <code>{{.Creator}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right"><span class="badge {{reqStatusClass .Status}}">{{reqStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有需求文档。点击「全部需求 →」创建本仓库的迭代需求。</div>
{{end}}
</div>
<div class="box">
<div class="box-header">{{oct "x"}} 缺陷 <span class="spacer"></span><a href="/repos/{{$full}}/bugs">全部缺陷 →</a></div>
{{range .Bugs}}
<div class="pl-row">
{{template "statusicon" (bugStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/bugs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span class="badge {{severityClass .Severity}}">{{severityText .Severity}}</span>
<span>指派给 <code>{{.Assignee}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right"><span class="badge {{bugStatusClass .Status}}">{{bugStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有缺陷记录。发现问题请点击「全部缺陷 →」提交，会指派给对应开发人员。</div>
{{end}}
</div>
<div class="box">
<div class="box-header">{{oct "play"}} 最近部署</div>
{{range .Tasks}}
<div class="pl-row">
{{template "statusicon" .Status}}
<div class="pl-main">
<div class="t">{{.Branch}}</div>
<div class="m"><span>{{oct "commit"}} <code>{{shal .SHA}}</code></span><span>{{ftime .CreatedAt}}</span></div>
</div>
<div class="right">{{fdur .Duration}}</div>
</div>
{{else}}
<div class="empty">还没有部署记录。开发推送后会自动构建部署，部署成功即可在这里打开测试。</div>
{{end}}
</div>
</div>
<div class="col-side">
<div class="box">
<div class="box-header">{{oct "play"}} 测试入口</div>
<div class="box-row" style="display:flex;flex-direction:column;gap:10px;align-items:stretch">
{{if or .StaticDir .Port}}
{{if .StaticDir}}<a href="/sites/{{.Repo.Slug}}/" class="btn btn-primary">{{oct "play"}} 打开 Web 站点</a>{{end}}
{{if .Port}}<a href="/api/{{.Repo.Slug}}/" class="btn btn-primary">{{oct "play"}} 测试后端 API</a>
<span class="muted">后端经平台代理访问（/api/{{.Repo.Slug}}/），无需直连端口。</span>{{end}}
{{else}}
<span class="muted">还没有可访问的部署。部署成功后这里会出现「打开 Web 站点 / 后端服务」的入口。</span>
{{end}}
</div>
</div>
<div class="box">
<div class="box-header">关于</div>
<div class="box-row muted" style="display:flex;flex-direction:column;gap:8px">
<span>{{oct "org"}} 归属 {{ownerLabel .Repo.Owner}}</span>
<span>{{oct "clock"}} 创建于 {{ftime .Repo.CreatedAt}}</span>
</div>
</div>
</div>
</div>
</main>{{template "foot"}}{{end}}

{{define "repo_commits"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
<div class="box">
<div class="box-header">{{oct "commit"}} 提交历史 · <code>{{.Branch}}</code></div>
{{range .Commits}}
<div class="commit">
<span class="avatar" style="background:{{avacolor .Author}}">{{initials .Author}}</span>
<div>
<div class="cmsg">{{.Msg}}</div>
<div class="cmeta"><span>{{.Author}}</span> 提交于 {{ftime .When}}</div>
</div>
<div class="csha"><a href="/repos/{{$.Repo.FullName}}/tree/{{.SHA}}" title="浏览此提交"><code>{{shal .SHA}}</code></a></div>
</div>
{{else}}
<div class="empty">没有提交。</div>
{{end}}
</div>
<p><a href="/repos/{{.Repo.FullName}}/tree/{{.Branch}}">← 返回代码浏览</a></p>
</main>{{template "foot"}}{{end}}

{{define "repo_tree"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{template "flash" .Base}}
{{$full := .Repo.FullName}}
{{$branch := .Branch}}
{{$ps := ""}}{{if .Path}}{{$ps = print "/" .Path}}{{end}}
<div class="box">
<div class="box-header">
<div class="crumbs">
<a href="/repos/{{$full}}/tree/{{$branch}}"><b>{{$branch}}</b></a>
{{range .Crumbs}}{{if .Final}}<span class="sep">/</span><span class="final">{{.Name}}</span>
{{else}}<span class="sep">/</span><a href="{{.URL}}">{{.Name}}</a>{{end}}{{end}}
</div>
<span class="spacer"></span>
{{if gt (len .Branches) 1}}
<select onchange="if(this.value)location='/repos/{{$full}}/tree/'+this.value+'{{$ps}}'">
{{range .Branches}}<option value="{{.Name}}" {{if eq .Name $branch}}selected{{end}}>{{.Name}}</option>{{end}}
</select>
{{end}}
</div>
{{range .Entries}}
<div class="file-row {{if .IsDir}}dir{{end}}">
{{if .IsDir}}{{oct "dir"}}{{else}}{{oct "file"}}{{end}}
<a class="fname" href="{{.URL}}">{{.Name}}</a>
<span class="fsize">{{.Size}}</span>
</div>
{{else}}
<div class="empty">目录为空。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "repo_blob"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
{{$branch := .Branch}}
{{$raw := print "/repos/" $full "/raw/" $branch "/" .Path}}
{{$suffix := ""}}{{if .Dir}}{{$suffix = print "/" .Dir}}{{end}}
<div class="blob-head">
<div class="crumbs">
<a href="/repos/{{$full}}/tree/{{$branch}}"><b>{{$branch}}</b></a>
{{range .Crumbs}}{{if .Final}}<span class="sep">/</span><span class="final">{{.Name}}</span>
{{else}}<span class="sep">/</span><a href="{{.URL}}">{{.Name}}</a>{{end}}{{end}}
</div>
<span class="right">
{{if .Language}}<span class="pill">{{.Language}}</span>{{end}}
<span class="muted">{{hsize .Size}}{{if and .Lines (not .Binary) (not .TooBig)}} · {{.Lines}} 行{{end}}</span>
{{if gt (len .Branches) 1}}
<select onchange="if(this.value)location='/repos/{{$full}}/tree/'+this.value+'{{$suffix}}'">
{{range .Branches}}<option value="{{.Name}}" {{if eq .Name $branch}}selected{{end}}>{{.Name}}</option>{{end}}
</select>
{{end}}
<a class="btn ghost btn-sm" href="{{$raw}}">Raw</a>
</span>
</div>
<div class="blob-body" id="codebox">
{{if .Binary}}<div class="empty">二进制文件 —— 使用上方 Raw 链接查看或下载。</div>
{{else if .TooBig}}<div class="empty">文件超过 512 KiB —— 使用上方 Raw 链接查看或下载。</div>
{{else}}<pre id="code" data-lang="{{.Language}}">{{.Content}}</pre>
<script>{{blobJS}}</script>{{end}}
</div>
<p style="margin-top:14px"><a href="/repos/{{$full}}/tree/{{$branch}}">← 返回 {{$branch}} 根目录</a></p>
</main>{{template "foot"}}{{end}}

{{define "repo_reqs"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
<div class="title-row" style="margin:4px 0 0">
<h1>需求文档</h1>
<span style="flex:1"></span>
<a href="/repos/{{$full}}/reqs/new" class="btn btn-primary">{{oct "file"}} 新建需求</a>
</div>
<p class="muted" style="margin:6px 0">
待开发 <b>{{.Pending}}</b> · 待核查 <b>{{.Review}}</b> · 已关闭 <b>{{.Closed}}</b>
&nbsp;|&nbsp; 过滤：
<a href="/repos/{{$full}}/reqs" {{if not .Filter}}style="color:var(--accent);font-weight:600"{{end}}>全部</a>
<a href="/repos/{{$full}}/reqs?status=pending" {{if eq .Filter "pending"}}style="color:var(--accent);font-weight:600"{{end}}>待开发</a>
<a href="/repos/{{$full}}/reqs?status=review" {{if eq .Filter "review"}}style="color:var(--accent);font-weight:600"{{end}}>待核查</a>
<a href="/repos/{{$full}}/reqs?status=closed" {{if eq .Filter "closed"}}style="color:var(--accent);font-weight:600"{{end}}>已关闭</a>
</p>
<div class="box">
{{range .Reqs}}
<div class="pl-row">
{{template "statusicon" (reqStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/reqs/{{.ID}}">{{.Title}}</a></div>
<div class="m"><span>创建者 <code>{{.Creator}}</code></span><span>{{ftime .CreatedAt}}</span>
{{if not .ClosedAt.IsZero}}<span>关闭于 {{ftime .ClosedAt}}</span>{{end}}</div>
</div>
<div class="right"><span class="badge {{reqStatusClass .Status}}">{{reqStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有需求文档。点击「新建需求」，以 markdown 编写本仓库的功能迭代需求；内容只有创建者可编辑，同组织所有成员可见。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "req_edit"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
{{if .IsNew}}<div class="title-row" style="margin:4px 0 0"><h1>新建需求</h1><span style="flex:1"></span>
<a href="/repos/{{$full}}/reqs" class="btn ghost">返回需求列表</a></div>{{else}}
<div class="title-row" style="margin:4px 0 0"><h1>编辑需求</h1><span style="flex:1"></span>
<a href="/repos/{{$full}}/reqs/{{.Req.ID}}" class="btn ghost">返回需求详情</a></div>{{end}}
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card" style="max-width:860px">
{{if .IsNew}}
<form method="post" action="/repos/{{$full}}/reqs/new">
{{else}}
<form method="post" action="/repos/{{$full}}/reqs/{{.Req.ID}}/edit">
{{end}}
<label>标题 *</label><input name="title" value="{{.Title}}" required placeholder="一句话概括这个需求">
<label>需求文档（markdown）*</label>
<textarea name="body" style="min-height:320px;font-family:var(--mono)" required placeholder="# 背景&#10;&#10;## 功能描述&#10;- …&#10;&#10;## 验收标准&#10;- [ ] …">{{.Body}}</textarea>
<p style="margin-top:14px"><button class="btn-primary">{{if .IsNew}}创建需求{{else}}保存修改{{end}}</button>
<span class="muted" style="margin-left:10px">内容只有创建者可编辑，其他人只读；创建后进入「待开发」。</span></p>
</form>
</div>
</main>{{template "foot"}}{{end}}

{{define "req_detail"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
{{$req := .Req}}
<div class="box">
<div class="bug-head">
{{template "statusicon" (reqStatusClass $req.Status)}}
<div style="flex:1;min-width:0">
<div class="t">{{$req.Title}}</div>
<div class="bug-meta" style="margin-top:6px">
<span class="badge {{reqStatusClass $req.Status}}">{{reqStatusText $req.Status}}</span>
<span>创建者 <code>{{$req.Creator}}</code></span>
<span>创建于 {{ftime $req.CreatedAt}}</span>
{{if not $req.ClosedAt.IsZero}}<span>关闭于 {{ftime $req.ClosedAt}}</span>{{end}}
</div>
</div>
<div style="display:flex;gap:8px;flex-wrap:wrap">
{{if .CanEdit}}<a href="/repos/{{$full}}/reqs/{{$req.ID}}/edit" class="btn">{{oct "gear"}} 编辑文档</a>{{end}}
{{if .CanReview}}<form method="post" action="/repos/{{$full}}/reqs/{{$req.ID}}/status">
<input type="hidden" name="to" value="review">
<button class="btn-primary">开发完成 → 待核查</button></form>{{end}}
{{if .CanReject}}<form method="post" action="/repos/{{$full}}/reqs/{{$req.ID}}/status">
<input type="hidden" name="to" value="pending">
<button class="danger">核查不通过，退回待开发</button></form>{{end}}
{{if and .CanClose (ne $req.Status "closed")}}<form method="post" action="/repos/{{$full}}/reqs/{{$req.ID}}/status">
<input type="hidden" name="to" value="closed">
<button class="btn-primary">{{if eq $req.Status "review"}}核查通过，关闭{{else}}作废需求，关闭{{end}}</button></form>{{end}}
</div>
</div>
<div class="box-row" style="border-top:1px solid var(--border2)">
<div class="md">{{.RenderedBody}}</div>
</div>
</div>

<div class="box">
<div class="box-header">状态流转记录（{{len .Records}}）</div>
{{range .Records}}
<div class="commit">
<span class="avatar" style="background:{{avacolor .Author}}">{{initials .Author}}</span>
<div style="min-width:0">
<div class="muted" style="font-size:12px">{{.Author}}</div>
<div class="md">{{renderMarkdown .Body}}</div>
</div>
<div class="csha muted">{{ftime .Time}}</div>
</div>
{{else}}
<div class="empty">暂无流转记录。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "repo_bugs"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
<div class="title-row" style="margin:4px 0 0">
<h1>缺陷列表</h1>
<span style="flex:1"></span>
<a href="/repos/{{$full}}/bugs/new" class="btn btn-primary">{{oct "x"}} 提交缺陷</a>
</div>
<p class="muted" style="margin:6px 0">
待修复 <b>{{.Open}}</b> · 待复测 <b>{{.Fixed}}</b> · 已关闭 <b>{{.Closed}}</b>
&nbsp;|&nbsp; 过滤：
<a href="/repos/{{$full}}/bugs" {{if not .Filter}}style="color:var(--accent);font-weight:600"{{end}}>全部</a>
<a href="/repos/{{$full}}/bugs?status=open" {{if eq .Filter "open"}}style="color:var(--accent);font-weight:600"{{end}}>待修复</a>
<a href="/repos/{{$full}}/bugs?status=fixed" {{if eq .Filter "fixed"}}style="color:var(--accent);font-weight:600"{{end}}>待复测</a>
<a href="/repos/{{$full}}/bugs?status=closed" {{if eq .Filter "closed"}}style="color:var(--accent);font-weight:600"{{end}}>已关闭</a>
</p>
<div class="box">
{{range .Bugs}}
<div class="pl-row">
{{template "statusicon" (bugStatusClass .Status)}}
<div class="pl-main">
<div class="t"><a href="/repos/{{$full}}/bugs/{{.ID}}">{{.Title}}</a>
<span class="badge {{severityClass .Severity}}">{{severityText .Severity}}</span></div>
<div class="m"><span>报告人 <code>{{.Reporter}}</code></span><span>指派给 <code>{{.Assignee}}</code></span>
<span>{{ftime .CreatedAt}}</span>{{if len .Files}}<span>{{oct "file"}} {{len .Files}} 张截图</span>{{end}}</div>
</div>
<div class="right"><span class="badge {{bugStatusClass .Status}}">{{bugStatusText .Status}}</span></div>
</div>
{{else}}
<div class="empty">还没有缺陷。测试发现问题后点击「提交缺陷」，统一以 markdown 文档提交并指派给开发人员。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "bug_new"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
<div class="title-row" style="margin:4px 0 0"><h1>提交缺陷</h1><span style="flex:1"></span>
<a href="/repos/{{$full}}/bugs" class="btn ghost">返回缺陷列表</a></div>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card" style="max-width:720px">
<form method="post" action="/repos/{{$full}}/bugs/new" enctype="multipart/form-data">
<label>标题 *</label><input name="title" required placeholder="一句话描述问题">
<label>严重程度</label>
<select name="severity">
<option value="blocker">严重 — 阻塞测试 / 核心功能不可用</option>
<option value="major" selected>一般 — 主流程可用但有明显问题</option>
<option value="minor">轻微 — 样式 / 文案 / 体验问题</option>
</select>
<label>指派给（开发人员）*</label>
<select name="assignee" required>
{{range .Candidates}}<option value="{{.Name}}">{{.Name}}</option>{{end}}
</select>
<label>复现步骤 *</label>
<textarea name="steps" required placeholder="1. 打开 …&#10;2. 点击 …&#10;3. 出现 …"></textarea>
<label>预期结果</label>
<textarea name="expected" placeholder="应该发生什么"></textarea>
<label>实际结果</label>
<textarea name="actual" placeholder="实际发生了什么"></textarea>
<label>备注</label>
<textarea name="note" placeholder="环境、浏览器版本、出现频率等（选填）"></textarea>
<label>截图（最多 9 张，单张 ≤ 8 MiB，png/jpeg/gif/webp）</label>
<div style="display:flex;flex-direction:column;gap:8px">
<input type="file" name="shot0" accept="image/png,image/jpeg,image/gif,image/webp">
<input type="file" name="shot1" accept="image/png,image/jpeg,image/gif,image/webp">
<input type="file" name="shot2" accept="image/png,image/jpeg,image/gif,image/webp">
</div>
<p style="margin-top:14px"><button class="btn-primary">提交缺陷</button>
<span class="muted" style="margin-left:10px">提交后进入「待修复」，由被指派的开发人员修复并重新部署后转「待复测」，由你复测后关闭。</span></p>
</form>
</div>
</main>{{template "foot"}}{{end}}

{{define "bug_detail"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{$full := .Repo.FullName}}
{{$bug := .Bug}}
<div class="box">
<div class="bug-head">
{{template "statusicon" (bugStatusClass $bug.Status)}}
<div style="flex:1;min-width:0">
<div class="t">{{$bug.Title}}</div>
<div class="bug-meta" style="margin-top:6px">
<span class="badge {{bugStatusClass $bug.Status}}">{{bugStatusText $bug.Status}}</span>
<span class="badge {{severityClass $bug.Severity}}">{{severityText $bug.Severity}}</span>
<span>报告人 <code>{{$bug.Reporter}}</code></span>
<span>指派给 <code>{{$bug.Assignee}}</code></span>
<span>创建于 {{ftime $bug.CreatedAt}}</span>
{{if not $bug.ClosedAt.IsZero}}<span>关闭于 {{ftime $bug.ClosedAt}}</span>{{end}}
</div>
</div>
<div style="display:flex;gap:8px;flex-wrap:wrap">
{{if .CanFix}}<form method="post" action="/repos/{{$full}}/bugs/{{$bug.ID}}/status">
<input type="hidden" name="to" value="fixed"><input type="hidden" name="note" value="">
<button class="btn-primary">修复完成，已重新部署 → 待复测</button></form>{{end}}
{{if .CanVerify}}<form method="post" action="/repos/{{$full}}/bugs/{{$bug.ID}}/status">
<input type="hidden" name="to" value="closed"><input type="hidden" name="note" value="复测通过">
<button class="btn-primary">复测通过，关闭</button></form>
<form method="post" action="/repos/{{$full}}/bugs/{{$bug.ID}}/status">
<input type="hidden" name="to" value="open"><input type="hidden" name="note" value="">
<button class="danger">复测不通过，重新打开</button></form>{{end}}
{{if .CanCloseInvalid}}<form method="post" action="/repos/{{$full}}/bugs/{{$bug.ID}}/status">
<input type="hidden" name="to" value="closed"><input type="hidden" name="note" value="误报 / 不再复现，直接关闭">
<button class="ghost">关闭（误报）</button></form>{{end}}
</div>
</div>
<div class="box-row" style="border-top:1px solid var(--border2)">
<div class="md">{{$bug.Title}}

{{.RenderedBody}}</div>
</div>
{{if len .Bug.Files}}
<div class="shots" style="border-top:1px solid var(--border2)">
{{range .Bug.Files}}<a href="{{$.FilesURLPrefix}}{{.}}" target="_blank" rel="noopener"><img src="{{$.FilesURLPrefix}}{{.}}" alt="{{.}}" loading="lazy"></a>{{end}}
</div>
{{end}}
</div>

<div class="box">
<div class="box-header">流转与讨论（{{len .Comments}}）</div>
{{range .Comments}}
<div class="commit">
<span class="avatar" style="background:{{avacolor .Author}}">{{initials .Author}}</span>
<div style="min-width:0">
{{if .Status}}<div class="muted" style="font-size:12px">{{.Author}} 状态流转</div>{{else}}<div class="muted" style="font-size:12px">{{.Author}}</div>{{end}}
<div class="md">{{.HTML}}</div>
</div>
<div class="csha muted">{{ftime .Time}}</div>
</div>
{{else}}
<div class="empty">暂无讨论。</div>
{{end}}
<div class="box-row" style="border-top:1px solid var(--border2)">
<form method="post" action="/repos/{{$full}}/bugs/{{$bug.ID}}/comment">
<textarea name="body" placeholder="补充信息 / 复测结论（支持 markdown）"></textarea>
<p style="margin-top:8px"><button class="btn-primary">添加评论</button></p>
</form>
</div>
</div>
</main>{{template "foot"}}{{end}}

{{define "repo_settings"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
{{template "flash" .Base}}
<div class="grid">
<div class="card">
<h2>基本设置</h2>
<form method="post" action="/repos/{{.Repo.Name}}/settings">
<input type="hidden" name="action" value="update">
<label>可见性</label>
<select name="visibility" style="width:100%">
<option value="private" {{if not .Repo.Public}}selected{{end}}>Private — 仅归属者与组织成员</option>
<option value="public" {{if .Repo.Public}}selected{{end}}>Public — 匿名可只读</option>
</select>
<label>部署分支（留空 = 任何含 deploy.yaml 的分支）</label>
<input name="deploy_branch" value="{{.Repo.DeployBranch}}">
<p style="margin-top:14px"><button class="btn-primary">保存修改</button></p>
</form>
</div>
<div class="card">
<h2>危险操作</h2>
<p class="muted">删除仓库会同时删除其 git 数据、构建工作区和已部署的应用，不可恢复。</p>
<form method="post" action="/repos/{{.Repo.Name}}/settings"
 onsubmit="return confirm('确认删除仓库 {{.Repo.Name}}？此操作不可恢复！')">
<input type="hidden" name="action" value="delete">
<button class="danger">删除仓库</button>
</form>
</div>
</div>
</main>{{template "foot"}}{{end}}

{{define "admintabs"}}<div class="tabs" style="margin:8px 0 4px">
<a href="/admin/users" class="{{if eq .Title "用户管理"}}active{{end}}">{{oct "org"}} 用户</a>
<a href="/admin/orgs" class="{{if eq .Title "组织管理"}}active{{end}}">{{oct "org"}} 组织</a>
</div>{{end}}

{{define "admin_users"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<h1>管理</h1>
{{template "admintabs" .Base}}
{{template "flash" .Base}}
<div class="box">
<div class="box-header">创建用户</div>
<div class="box-row">
<form method="post" action="/admin/users" style="display:flex;gap:10px;flex-wrap:wrap;align-items:flex-end">
<div><label>用户名</label><input name="name" required></div>
<div><label>初始密码</label><input name="password" type="password" required></div>
<div><label>角色</label>
<select name="role">
<option value="developer">开发者 — 代码/流水线/部署</option>
<option value="tester">测试 — 仅站点与服务</option>
</select></div>
<div><label style="display:flex;gap:6px;align-items:center;margin:12px 0 0">管理员 <input type="checkbox" name="admin" style="width:auto"></label></div>
<button class="btn-primary" style="margin-bottom:1px">创建</button>
</form>
<div class="box-row muted" style="padding:8px 0 0">测试角色：只能打开已部署的站点与服务，无法查看源码、克隆仓库或进入流水线；勾选「管理员」后角色设置不生效。</div>
</div>
</div>
<div class="box">
<div class="box-header">全部用户（{{len .Users}}）</div>
<table>
<tr><th>用户</th><th>角色</th><th>组织</th><th>创建时间</th><th>操作</th></tr>
{{$self := .Self}}
{{range .Users}}
<tr>
<td><span class="avatar" style="background:{{avacolor .Name}}">{{initials .Name}}</span>
<code>{{.Name}}</code>{{if eq .Name $self}} <span class="muted">（你自己）</span>{{end}}</td>
<td>
{{if .Admin}}<span class="badge success">管理员</span>
{{if and (ne .Name $self) .Admin}}<form class="inline" method="post" action="/admin/users/{{.Name}}/admin">
<input type="hidden" name="admin" value="0"><button class="ghost btn-sm">撤销</button></form>{{end}}
{{else}}
<form class="inline" method="post" action="/admin/users/{{.Name}}/role">
<select name="role" onchange="this.form.submit()">
<option value="developer" {{if ne .Role "tester"}}selected{{end}}>开发者</option>
<option value="tester" {{if eq .Role "tester"}}selected{{end}}>测试</option>
</select></form>
<form class="inline" method="post" action="/admin/users/{{.Name}}/admin">
<input type="hidden" name="admin" value="1"><button class="ghost btn-sm">设为管理员</button></form>
{{end}}
</td>
<td>{{orgsOf $.Orgs .Name}}</td>
<td class="muted">{{ftime .CreatedAt}}</td>
<td>
<form class="inline" method="post" action="/admin/users/{{.Name}}/reset">
<input name="password" type="password" placeholder="新密码" required>
<button class="ghost btn-sm">重置密码</button></form>
<form class="inline" method="post" action="/admin/users/{{.Name}}/delete"
 onsubmit="return confirm('删除用户 {{.Name}}？')"><button class="danger btn-sm">删除</button></form>
</td>
</tr>
{{end}}
</table>
</div>
</main>{{template "foot"}}{{end}}

{{define "admin_orgs"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<h1>管理</h1>
{{template "admintabs" .Base}}
{{template "flash" .Base}}
<div class="box">
<div class="box-header">新增组织</div>
<div class="box-row">
<form method="post" action="/admin/orgs" style="display:flex;gap:10px;align-items:flex-end">
<div><label>组织名</label><input name="name" required></div>
<button class="btn-primary" style="margin-bottom:1px">新增组织</button>
</form>
</div>
<div class="box-row muted">组织是仓库的归属单位：组织下的用户可以看到并推送该组织管理的所有仓库；不同组织之间代码相互隔离。组织下没有仓库时才能删除。</div>
</div>
{{$users := .Users}}
{{range $o := .Orgs}}
<div class="box">
<div class="box-header">{{oct "org"}} <b style="color:var(--fg)">{{$o.Name}}</b>
<span class="pill">{{len $o.Members}} 成员</span>
<span class="pill">{{index $.RepoCount $o.Name}} 仓库</span>
{{if $o.Creator}}<span class="pill">创建者 {{$o.Creator}}</span>{{end}}</div>
<div class="box-row">
{{range $o.Members}}
<form class="inline" method="post" action="/admin/orgs/{{$o.Name}}/members"
 onsubmit="return confirm('从 {{$o.Name}} 移除 {{.}}？')" style="margin:2px 6px 2px 0">
<input type="hidden" name="action" value="remove">
<input type="hidden" name="member" value="{{.}}">
<span class="avatar" style="background:{{avacolor .}}">{{initials .}}</span>
<code>{{.}}</code> <button class="ghost btn-sm">移除</button></form>
{{else}}<span class="muted">还没有成员，从下方添加用户。</span>{{end}}
</div>
<div class="box-row" style="display:flex;gap:10px;align-items:flex-end;flex-wrap:wrap">
<form method="post" action="/admin/orgs/{{$o.Name}}/members" style="display:flex;gap:10px;align-items:flex-end">
<input type="hidden" name="action" value="add">
<div><label>添加成员</label>
<select name="member">{{range $users}}<option value="{{.Name}}">{{.Name}}</option>{{end}}</select></div>
<button style="margin-bottom:1px">添加</button>
</form>
<span style="flex:1"></span>
<form method="post" action="/admin/orgs/{{$o.Name}}/delete" onsubmit="return confirm('删除组织 {{$o.Name}}？')">
<button class="danger">删除组织</button>
</form>
</div>
</div>
{{end}}
{{if not .Orgs}}<div class="box"><div class="empty">还没有组织。新增组织后，创建仓库时即可把仓库归属给组织。</div></div>{{end}}
</main>{{template "foot"}}{{end}}

{{define "pipelines"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<div class="title-row" style="margin-bottom:4px">
<h1>{{if .Repo}}<a href="/repos/{{.Repo}}" style="color:inherit">{{.Repo}}</a> <span class="sep">/</span> 流水线{{else}}流水线{{end}}</h1>
<span style="flex:1"></span>
<span class="muted">running {{.Running}} · pending {{.Pending}}</span>
</div>
{{template "flash" .Base}}
{{if .Repo}}<p class="muted">仅显示仓库 <code>{{.Repo}}</code> 的流水线 · <a href="/repos/{{.Repo}}">← 返回仓库</a></p>{{end}}
<div class="box">
{{range .Tasks}}
{{template "plrow" .}}
{{else}}
<div class="empty">还没有部署记录。在仓库页点击「重新部署」，或 push 一个包含 deploy.yaml 的提交即可触发流水线。</div>
{{end}}
</div>
</main>{{template "foot"}}{{end}}

{{define "pipeline_detail"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
{{$t := .Task}}
<div class="pl-row" style="border:1px solid var(--border);border-radius:var(--radius);padding:16px">
{{template "statusicon" .Task.Status}}
<div class="pl-main">
<div class="t" style="font-size:15px">
<a href="/repos/{{.Task.Repo}}">{{.Task.Repo}}</a> <span class="sep">/</span> {{.Task.Branch}}
<span class="badge {{.Task.Status}}" id="status-badge" style="margin-left:8px">{{.Task.Status}}</span></div>
<div class="m">
<span>{{oct "commit"}} <code>{{shal .Task.SHA}}</code></span>
<span>{{oct "clock"}} {{ftime .Task.CreatedAt}} → {{ftime .Task.FinishedAt}}</span>
<span>耗时 {{fdur .Task.Duration}}</span>
</div>
</div>
</div>
{{if .Task.Error}}<div class="err" id="task-error">{{.Task.Error}}</div>{{end}}
{{if and .CanWrite (ne .Task.Status "running") (ne .Task.Status "pending")}}
<form method="post" action="/repos/{{.Task.Repo}}/deploy" style="margin:10px 0">
<input type="hidden" name="sha" value="{{.Task.SHA}}">
<button class="btn">{{oct "play"}} 回到此版本（重新部署 {{shal .Task.SHA}}）</button>
<span class="muted">将该历史提交重新构建部署，用于回滚。</span></form>{{end}}

<div class="box">
<div class="box-header">步骤</div>
<div style="padding:6px 16px">
{{range .Task.Steps}}
<div class="step">
{{if .OK}}<span class="status-icon success">{{oct "check"}}</span>{{else}}<span class="status-icon failed">{{oct "x"}}</span>{{end}}
<span class="sname">{{.Name}}</span>
<span class="sdur">{{fdur (durOf .Start .End)}}</span>
{{if .Detail}}<span class="sdetail">{{.Detail}}</span>{{end}}
</div>
{{end}}
</div>
</div>

<div class="box">
<div class="box-header">任务日志 <span class="spacer"></span><a href="/api/tasks/{{$t.ID}}/log">JSON</a></div>
<div style="border-top:1px solid var(--border2)"><pre id="log">{{.LogTail}}</pre></div>
</div>

<script>
(function(){
  var initial = "{{.Task.Status}}";
  var done = !(initial === "running" || initial === "pending");
  if (done) return; // 页面已是最终状态：不轮询、不刷新
  (function poll(){
    fetch('/api/tasks/{{$t.ID}}/log').then(r=>r.json()).then(d=>{
      document.getElementById('log').textContent = d.log || '（日志文件已清理或尚无输出）';
      var b = document.getElementById('status-badge');
      b.textContent = d.status; b.className = 'badge ' + d.status;
      if (d.status === 'running' || d.status === 'pending') { setTimeout(poll, 2000); }
      else { setTimeout(()=>location.reload(), 800); } // 完成后刷新一次以显示步骤
    }).catch(()=>setTimeout(poll, 3000));
  })();
})();
</script>
</main>{{template "foot"}}{{end}}

{{define "message"}}
{{template "head" .Base}}{{template "nav" .Base}}<main>
<div class="box"><div class="box-header">{{.Title}}</div>
<div class="box-row" style="padding:24px">{{.Body}}</div></div>
</main>{{template "foot"}}{{end}}

{{define "applog"}}
{{template "head" .Base}}{{template "nav" .Base}}
{{template "repohead" .}}
<main>
<div class="box">
<div class="box-header">{{oct "file"}} 运行日志 <code>app.log</code>
<span class="spacer"></span>
{{if .Exists}}<span class="muted" id="log-meta"></span>
<button class="btn ghost" type="button" id="log-refresh">刷新最新</button>
<button class="btn ghost" type="button" id="log-more"{{if not .HasMore}} disabled{{end}}>加载更早</button>{{end}}
</div>
{{if .Exists}}
<div style="border-top:1px solid var(--border2)"><pre id="log" style="margin:0;max-height:70vh;overflow:auto;background:var(--inset);border:0;border-radius:0">{{.Lines}}</pre></div>
{{else}}
<div class="empty">{{.Reason}}</div>
{{end}}
</div>
<p class="muted" style="font-size:12.5px">路径：<code>{{.LogPath}}</code>　（服务 stdout/stderr 追加写入；程序自带日志系统写的文件不在这里）</p>
{{if .Exists}}
<script>
(function(){
  var api = "{{.APIURL}}";
  var pre = document.getElementById("log");
  var meta = document.getElementById("log-meta");
  var more = document.getElementById("log-more");
  var refresh = document.getElementById("log-refresh");
  var start = {{.Start}};
  function fmt(n){var u=["B","KB","MB","GB"],i=0;while(n>=1024&&i<u.length-1){n/=1024;i++;}return (i?n.toFixed(1):n)+" "+u[i];}
  function setMeta(s,m){if(meta)meta.textContent="大小 "+fmt(s)+(m?" · 还可加载更早":"");if(more)more.disabled=!m;}
  function fetchPage(before){
    var u = api + "?lines=200" + (before > 0 ? "&before=" + before : "");
    return fetch(u, {credentials:"same-origin"}).then(function(r){return r.json();});
  }
  setMeta({{.Size}}, {{if .HasMore}}true{{else}}false{{end}});
  if (more) more.onclick = function(){
    if (more.disabled) return;
    more.disabled = true;
    fetchPage(start).then(function(d){
      if (!d.exists) return;
      pre.textContent = d.lines.join("\n") + "\n" + pre.textContent;
      start = d.start; setMeta(d.size, d.has_more);
    }).catch(function(){ more.disabled = false; });
  };
  if (refresh) refresh.onclick = function(){
    fetchPage(0).then(function(d){
      if (!d.exists) return;
      pre.textContent = d.lines.join("\n");
      start = d.start; setMeta(d.size, d.has_more);
    });
  };
})();
</script>
{{end}}
</main>{{template "foot"}}{{end}}
`))

// ---- page data structs ----

type loginData struct {
	Base     base
	Error    string
	Username string
}

type meKeysData struct {
	Base  base
	Keys  []store.SSHKey
	Error string
}

type meData struct {
	Base  base
	User  store.User
	Orgs  []string
	Error string
}

type dashData struct {
	Base     base
	Repos    []repoRow
	LoggedIn bool
	Admin    bool
	Tester   bool
	// 我的待办（登录用户）：指派给我的待修复缺陷 / 我提交的待复测缺陷 / 待我核查的需求
	TodoBugsFix   []store.Bug
	TodoBugsTest  []store.Bug
	TodoReqsCheck []store.Requirement
}

type repoNewData struct {
	Base   base
	Owners []ownerOption
	Error  string
	Name   string
}

// appLogData 是仓库运行日志页（app.log）的数据。
type appLogData struct {
	Base base
	RepoPage
	Exists  bool
	Reason  string
	Lines   string // 已拼接好的最新一页（服务端渲染，无 JS 也能看）
	Size    int64
	Start   int64
	HasMore bool
	LogPath string
	APIURL  string
}

// RepoPage carries the fields the shared repo header (repohead template)
// needs. Every repo sub-page data struct embeds it — a missing field here
// breaks the whole page with a template evaluation error.
type RepoPage struct {
	Repo      store.Repo
	CanManage bool
	CanWrite  bool
	HasSite   bool
	Head      string
	QA        bool   // 测试角色：隐藏源码类 tab
	Tab       string // "code" | "files" | "commits" | "settings" | "qa"
}

// WithTab returns a copy of page with Tab set (for per-page header states).
func (p RepoPage) WithTab(tab string) RepoPage {
	p.Tab = tab
	return p
}

type repoOverviewData struct {
	Base base
	RepoPage
	Branches       []branchInfo
	README         string
	RenderedReadme template.HTML // README.md 的 markdown 渲染结果
	ProtectedNote  string        // 组织仓库的主分支保护提示（非组织仓库为空）
	Bugs           []store.Bug
	Reqs           []store.Requirement
	Tasks          []model.Task
	StaticDir      string
	Port           int
	APIURL         string // /api/<slug>/（有后端服务时非空）
	SSHURL         string // ssh://git@host:port/owner/repo.git（未启用 SSH 时为空）
}

type commitRow struct {
	SHA    string
	Author string
	When   time.Time
	Msg    string
}

type repoCommitsData struct {
	Base base
	RepoPage
	Branch  string
	Commits []commitRow
}

// repoTesterData 是测试角色的仓库页：部署状态与测试入口，无源码相关信息。
type repoTesterData struct {
	Base base
	RepoPage
	Reqs      []store.Requirement
	Bugs      []store.Bug
	Tasks     []model.Task
	StaticDir string
	Port      int
	APIURL    string
}

// crumb is one breadcrumb segment in the file browser; Final segments are
// rendered as plain text (the current location), others as links.
type crumb struct {
	Name  string
	URL   string
	Final bool
}

type treeEntry struct {
	Name  string
	IsDir bool
	Size  string // 人类可读大小；目录为 "-"
	URL   string
}

type repoTreeData struct {
	Base base
	RepoPage
	Branch   string
	Path     string // 当前目录；用于分支切换下拉保留路径
	Crumbs   []crumb
	Entries  []treeEntry
	Branches []branchInfo // 分支切换下拉（多于 1 个分支时显示）
}

type repoBlobData struct {
	Base base
	RepoPage
	Branch   string
	Path     string // 用于构造 raw 链接
	Dir      string // 所在目录；分支切换下拉跳到对应目录
	Language string // codeLang 识别的语言（blobJS 高亮用），空为通用规则
	Crumbs   []crumb
	Size     int64
	Lines    int
	Content  string
	Binary   bool
	TooBig   bool
	Branches []branchInfo // 分支切换下拉（多于 1 个分支时显示）
}

type repoSettingsData struct {
	Base base
	RepoPage
}

type adminUsersData struct {
	Base  base
	Users []store.User
	Orgs  []store.Org
	Msg   string
	Self  string
}

type adminOrgsData struct {
	Base      base
	Orgs      []store.Org
	Users     []store.User
	RepoCount map[string]int
	Msg       string
}

type pipelinesData struct {
	Base    base
	Repo    string // 可选：只显示该仓库（owner/name）的流水线
	Tasks   []model.Task
	Running int
	Pending int
	Msg     string
}

type pipelineDetailData struct {
	Base     base
	Task     model.Task
	LogTail  string
	CanWrite bool // 回滚（重新部署该提交）按钮
}

type messageData struct {
	Base  base
	Title string
	Body  string
}

// ---- 缺陷管理 ----

type repoBugsData struct {
	Base base
	RepoPage
	Bugs   []store.Bug
	Filter string // 可选：按状态过滤 open/fixed/closed
	Open   int
	Fixed  int
	Closed int
	Tester bool
}

type bugNewData struct {
	Base base
	RepoPage
	Candidates []store.User // 可指派的开发人员（对该仓库有写权限且非测试角色）
	Error      string
}

// ---- 需求管理 ----

type repoReqsData struct {
	Base base
	RepoPage
	Reqs    []store.Requirement
	Filter  string
	Pending int
	Review  int
	Closed  int
}

type reqEditData struct {
	Base base
	RepoPage
	Req   store.Requirement
	IsNew bool
	Title string
	Body  string
	Error string
}

type reqDetailData struct {
	Base base
	RepoPage
	Req          store.Requirement
	RenderedBody template.HTML
	Records      []store.ReqRecord
	CanEdit      bool // 仅创建者
	CanReview    bool // 有写权限者（代码提交者）/管理员
	CanClose     bool // 仅创建者（验收通过 / 作废）
	CanReject    bool // 仅创建者（核查不通过退回）
}

type bugDetailData struct {
	Base base
	RepoPage
	Bug             store.Bug
	RenderedBody    template.HTML
	Comments        []renderedComment
	CanFix          bool // 被指派人/管理员：标记修复完成
	CanVerify       bool // 报告人/管理员：复测通过关闭 / 不通过重开
	CanCloseInvalid bool // 报告人/管理员：误报直接关闭
	FilesURLPrefix  string
}
