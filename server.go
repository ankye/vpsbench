package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---- 帮助函数 ----

func osHostname() (string, error) { return os.Hostname() }
func numCPU() int                 { return runtime.NumCPU() }

var sink atomic.Uint64 // 防止编译器把烧 CPU 循环优化掉

// burn 执行 n 步固定开销的整数运算,可跨机器横向比较
func burn(n int) {
	x := uint64(1)
	for i := 0; i < n; i++ {
		x = x*6364136223846793005 + 1442695040888963407
		x ^= x >> 13
	}
	sink.Add(x)
}

func randBuf(n int) []byte {
	b := make([]byte, n)
	x := uint64(0x9E3779B97F4A7C15)
	for i := range b {
		x = x*6364136223846793005 + 1442695040888963407
		b[i] = byte(x >> 56)
	}
	return b
}

// ---- API 响应结构(客户端共用) ----

type infoResp struct {
	Hostname    string  `json:"hostname"`
	Kernel      string  `json:"kernel"`
	NumCPU      int     `json:"num_cpu"`
	GOMAXPROCS  int     `json:"gomaxprocs"`
	MemTotalMB  int64   `json:"mem_total_mb"`
	MemAvailMB  int64   `json:"mem_avail_mb"`
	UptimeSec   float64 `json:"uptime_sec"`
	GoVersion   string  `json:"go_version"`
}

type cpuResp struct {
	Ops       uint64  `json:"ops"`
	Ms        float64 `json:"ms"`
	OpsPerSec float64 `json:"ops_per_sec"`
}

type fillResp struct {
	ReqCores        int     `json:"req_cores"`
	Ms              float64 `json:"ms"`
	CPUSec          float64 `json:"cpu_sec"`          // 实际拿到的 CPU 时间
	EffectiveCores  float64 `json:"effective_cores"`  // 实际可用核数 = cpu_sec / wall
	OpsPerCorePerSec float64 `json:"ops_per_core_sec"` // 满载时单核算力
}

type memResp struct {
	MB      int     `json:"mb"`
	TouchMs float64 `json:"touch_ms"`
	MBps    float64 `json:"mbps"`
}

type fsyncStat struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

type diskResp struct {
	SeqMB       int       `json:"seq_mb"`
	WriteMs     float64   `json:"write_ms"`
	FsyncMs     float64   `json:"fsync_ms"`
	ReadMs      float64   `json:"read_ms"`
	WriteMBps   float64   `json:"write_mbps"`
	ReadMBps    float64   `json:"read_mbps"`
	SmallFiles  int       `json:"small_files"`
	Fsync       fsyncStat `json:"fsync"`
}

type uploadResp struct {
	Bytes uint64  `json:"bytes"`
	Sec   float64 `json:"sec"`
	MBps  float64 `json:"mbps"`
}

type errResp struct {
	Error string `json:"error"`
}

// ---- 服务端 ----

var (
	diskDir     string
	globalToken string
	streamBuf   = randBuf(1 << 20)
	mon         *Monitor
)

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", ":8300", "监听地址")
	dir := fs.String("dir", "", "磁盘测试目录(默认系统临时目录)")
	tok := fs.String("token", "", "访问令牌,设置后所有请求需带 ?token= 或 X-Token 头")
	fs.Parse(args)

	diskDir = *dir
	if diskDir == "" {
		diskDir = os.TempDir()
	}
	globalToken = *tok
	mon = NewMonitor()
	mon.Start()

	total, avail := readMemKB()
	fmt.Printf(`vpsbench 服务端已启动(Gin + 内嵌网页)
  监听     : %s
  CPU      : %d 核 | Go %d
  内存     : %d MB / %d MB
  磁盘目录 : %s
  令牌     : %s

▶ 浏览器打开  http://<本机公网IP>%s  点按钮即可测试
▶ 内网/命令行: ./vpsbench all http://<本机IP>%s
提示: 防火墙记得放行端口(如 ufw allow 8300);测完记得关服务
`, *addr, runtime.NumCPU(), runtime.GOMAXPROCS(0), avail/1024, total/1024, diskDir,
		func() string {
			if globalToken == "" {
				return "未设置(公开访问)"
			}
			return "已启用"
		}(), normPort(*addr), normPort(*addr))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// normPort 从监听地址里取端口部分用于拼展示 URL("127.0.0.1:8300"→":8300")
func normPort(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return ""
	}
	if addr[i:] == ":80" {
		return ""
	}
	return addr[i:]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func qInt(r *http.Request, key string, def, mn, mx int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < mn {
		return mn
	}
	if n > mx {
		return mx
	}
	return n
}

// ---- handlers ----

func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"t":%d}`, time.Now().UnixNano())
}

// handleEcho 返回指定字节数的随机数据(1B~64KB),用于不同包尺寸下的并发能力测试
func handleEcho(w http.ResponseWriter, r *http.Request) {
	size := qInt(r, "size", 1024, 1, 65536)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Write(streamBuf[:size])
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	total, avail := readMemKB()
	host, _ := os.Hostname()
	writeJSON(w, 200, infoResp{
		Hostname: host, Kernel: kernelVersion(),
		NumCPU: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0),
		MemTotalMB: total / 1024, MemAvailMB: avail / 1024,
		UptimeSec: hostUptimeSec(), GoVersion: runtime.Version(),
	})
}

func handleCPU(w http.ResponseWriter, r *http.Request) {
	ms := qInt(r, "ms", 1000, 10, 60000)
	budget := time.Duration(ms) * time.Millisecond
	t0 := time.Now()
	var ops uint64
	for time.Since(t0) < budget {
		burn(20000)
		ops += 20000
	}
	el := time.Since(t0)
	writeJSON(w, 200, cpuResp{Ops: ops, Ms: msF(el), OpsPerSec: float64(ops) / el.Seconds()})
}

func handleFill(w http.ResponseWriter, r *http.Request) {
	n := qInt(r, "n", runtime.NumCPU(), 1, 128)
	ms := qInt(r, "ms", 5000, 100, 120000)
	u0, s0, ok := readSelfCPUTicks()
	t0 := time.Now()
	deadline := t0.Add(time.Duration(ms) * time.Millisecond)
	var total atomic.Uint64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local uint64
			for time.Now().Before(deadline) {
				burn(50000)
				local += 50000
			}
			total.Add(local)
		}()
	}
	wg.Wait()
	el := time.Since(t0)

	cpuSec := 0.0
	if u1, s1, ok2 := readSelfCPUTicks(); ok && ok2 {
		cpuSec = float64((u1+s1)-(u0+s0)) / 100 // 100 ticks = 1 CPU 秒
	}
	if cpuSec < 0 || cpuSec > el.Seconds()*float64(n+1) {
		cpuSec = 0
	}
	eff := 0.0
	if el.Seconds() > 0 {
		eff = cpuSec / el.Seconds()
	}
	opsPerCore := 0.0
	if el.Seconds() > 0 {
		opsPerCore = float64(total.Load()) / el.Seconds() / float64(n)
	}
	writeJSON(w, 200, fillResp{
		ReqCores: n, Ms: msF(el), CPUSec: cpuSec,
		EffectiveCores: eff, OpsPerCorePerSec: opsPerCore,
	})
}

func handleMem(w http.ResponseWriter, r *http.Request) {
	mb := qInt(r, "mb", 256, 1, 8192)
	_, availKB := readMemKB()
	if int64(mb)*1024 > availKB-512*1024 {
		writeJSON(w, 400, errResp{Error: fmt.Sprintf("可用内存不足(可用 %d MB)", availKB/1024)})
		return
	}
	b := make([]byte, mb<<20)
	t0 := time.Now()
	for i := 0; i < len(b); i += 4096 { // 逐页写入,强制真实分配物理页
		b[i] = 1
	}
	el := time.Since(t0)
	writeJSON(w, 200, memResp{MB: mb, TouchMs: msF(el), MBps: float64(mb) / el.Seconds()})
	runtime.KeepAlive(b)
}

func handleDisk(w http.ResponseWriter, r *http.Request) {
	mb := qInt(r, "mb", 64, 1, 2048)
	files := qInt(r, "files", 100, 0, 2000)
	kb := qInt(r, "kb", 4, 1, 1024)

	buf := randBuf(1 << 20)
	f, err := os.CreateTemp(diskDir, "vpsbench-seq-")
	if err != nil {
		writeJSON(w, 500, errResp{Error: "创建文件失败: " + err.Error()})
		return
	}
	name := f.Name()
	defer os.Remove(name)

	// 顺序写
	t0 := time.Now()
	for i := 0; i < mb; i++ {
		if _, err := f.Write(buf); err != nil {
			writeJSON(w, 500, errResp{Error: "写失败: " + err.Error()})
			return
		}
	}
	writeEl := time.Since(t0)

	// fsync
	t1 := time.Now()
	if err := f.Sync(); err != nil {
		writeJSON(w, 500, errResp{Error: "fsync 失败: " + err.Error()})
		return
	}
	fsyncEl := time.Since(t1)
	f.Close()

	// 顺序读(受页缓存影响,仅作参考)
	t2 := time.Now()
	f2, err := os.Open(name)
	if err == nil {
		rb := make([]byte, 1<<20)
		for i := 0; i < mb; i++ {
			if _, err := io.ReadFull(f2, rb); err != nil {
				break
			}
		}
		f2.Close()
	}
	readEl := time.Since(t2)

	// 小文件 fsync 延迟(共享宿主机 I/O 争抢最敏感的指标)
	var lats []float64
	if files > 0 {
		small := randBuf(kb << 10)
		for i := 0; i < files; i++ {
			g, err := os.CreateTemp(diskDir, "vpsbench-sf-")
			if err != nil {
				break
			}
			s := time.Now()
			g.Write(small)
			g.Sync()
			g.Close()
			os.Remove(g.Name())
			lats = append(lats, msF(time.Since(s)))
		}
	}
	st := statsOf(lats)
	writeJSON(w, 200, diskResp{
		SeqMB: mb, WriteMs: msF(writeEl), FsyncMs: msF(fsyncEl), ReadMs: msF(readEl),
		WriteMBps: float64(mb) / writeEl.Seconds(), ReadMBps: float64(mb) / readEl.Seconds(),
		SmallFiles: len(lats),
		Fsync:      fsyncStat{P50: st.P50, P95: st.P95, P99: st.P99, Max: st.Max},
	})
}

func handleStream(w http.ResponseWriter, r *http.Request) {
	mb := qInt(r, "mb", 50, 1, 8192)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(mb<<20))
	for i := 0; i < mb; i++ {
		if _, err := w.Write(streamBuf); err != nil {
			return
		}
	}
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	n, _ := io.Copy(io.Discard, r.Body)
	n64 := uint64(0)
	if n > 0 {
		n64 = uint64(n)
	}
	el := time.Since(t0)
	mbps := 0.0
	if el.Seconds() > 0 {
		mbps = float64(n) / 1e6 / el.Seconds()
	}
	writeJSON(w, 200, uploadResp{Bytes: n64, Sec: el.Seconds(), MBps: mbps})
}

// handleHold 立刻返回响应头,hold 秒后再返回 body —— 用于最大并发连接数测试
func handleHold(w http.ResponseWriter, r *http.Request) {
	sec := qInt(r, "sec", 30, 1, 600)
	w.WriteHeader(http.StatusOK)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	time.Sleep(time.Duration(sec) * time.Second)
	fmt.Fprint(w, "ok")
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, mon.Snapshot(r.URL.Query().Get("full") == "1"))
}
