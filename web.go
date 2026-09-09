package main

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed index.html
var indexHTML []byte

// newRouter 构建 gin 路由:内嵌 HTML 控制台 + 全部测试 API
func newRouter() *gin.Engine {
	if mon == nil { // 进程内/免 server 模式也要有监控
		mon = NewMonitor()
		mon.Start()
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	auth := func(c *gin.Context) {
		if globalToken != "" && c.Query("token") != globalToken && c.GetHeader("X-Token") != globalToken {
			c.JSON(http.StatusUnauthorized, errResp{Error: "invalid token"})
			c.Abort()
			return
		}
	}
	// 复用 http.HandlerFunc 风格的 handlers(gin.Writer 实现了 http.Flusher 等)
	h := func(f func(http.ResponseWriter, *http.Request)) gin.HandlerFunc {
		return func(c *gin.Context) { f(c.Writer, c.Request) }
	}

	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML) })

	api := r.Group("/api", auth)
	api.GET("/ping", h(handlePing))
	api.GET("/echo", h(handleEcho))
	api.GET("/info", h(handleInfo))
	api.GET("/cpu", h(handleCPU))
	api.GET("/fill", h(handleFill))
	api.GET("/mem", h(handleMem))
	api.GET("/disk", h(handleDisk))
	api.GET("/stream", h(handleStream))
	api.POST("/upload", h(handleUpload))
	api.GET("/hold", h(handleHold))
	api.GET("/stats", h(handleStats))
	api.GET("/diag", h(handleDiag))
	return r
}
