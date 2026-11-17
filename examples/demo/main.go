// demo 是一个用于验证 gitwo 部署闭环的极简服务：
//   - /          服务 ./static 下的前端文件（如有）
//   - /api/info  返回构建/部署注入的环境信息
//
// 服务监听 $PORT（由 gitwo 自动分配并注入）。
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"app":    "demo",
			"commit": os.Getenv("GITWO_COMMIT"),
			"task":   os.Getenv("GITWO_TASK"),
			"branch": os.Getenv("GITWO_BRANCH"),
		})
	})
	log.Printf("demo listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
