/* Gitwo docs: 共享导航与页脚（改这一个文件即可调整全站菜单） */
(function () {
  var PAGES = [
    ["screens.html",    "界面"],
    ["features.html",   "功能"],
    ["start.html",      "快速开始"],
    ["roles.html",      "权限"],
    ["git.html",        "Git"],
    ["workflow.html",   "协作"],
    ["deploy-yaml.html","deploy.yaml"],
    ["hosting.html",    "托管"],
    ["cli.html",        "命令行"],
    ["faq.html",        "FAQ"]
  ];
  var here = (location.pathname.split("/").pop() || "index.html").toLowerCase();
  if (!here || here === "index" || here === "") here = "index.html";
  var LOGO = '<svg viewBox="0 0 16 16" width="20" height="20" fill="currentColor"><path d="M2 2.5A2.5 2.5 0 0 1 4.5 0h8.75a.75.75 0 0 1 .75.75v12.5a.75.75 0 0 1-.75.75h-2.5a.75.75 0 0 1 0-1.5h1.75v-2h-8a1 1 0 0 0-.714 1.7.75.75 0 1 1-1.072 1.05A2.495 2.495 0 0 1 2 11.5Zm10.5-1h-8a1 1 0 0 0-1 1v6.708A2.486 2.486 0 0 1 4.5 9h8ZM5 12.25a.25.25 0 0 1 .25-.25h3.5a.25.25 0 0 1 .25.25v3.25a.25.25 0 0 1-.4.2l-1.45-1.087a.249.249 0 0 0-.2 0L8.5 15.7a.25.25 0 0 1-.4-.2Z"/></svg>';

  var nav = document.getElementById("site-nav");
  if (nav) {
    var links = PAGES.map(function (p) {
      return '<a class="lnk' + (p[0] === here ? " on" : "") + '" href="' + p[0] + '">' + p[1] + "</a>";
    }).join("");
    nav.innerHTML = '<div class="in"><a class="brand" href="index.html">' + LOGO + 'Gitwo</a>' + links +
      '<span class="sp"></span><a class="btn ghost" href="https://github.com/flairamos/gitwo">GitHub</a></div>';
  }
  var ft = document.getElementById("site-footer");
  if (ft) {
    ft.innerHTML = '<div class="wrap in"><span>Gitwo · 自包含 Git 服务端 + CI/CD 平台</span><span>·</span>' +
      '<a href="index.html">首页</a><a href="https://github.com/flairamos/gitwo/blob/main/README.md">README</a>' +
      '<a href="https://github.com/flairamos/gitwo">GitHub</a>' +
      '<a href="https://github.com/flairamos/gitwo/blob/main/LICENSE">Apache-2.0</a></div>';
  }
})();
