package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---- 公共小工具 ----

var clientToken string

// reorderArgs 把 "URL 在前、flag 在后" 的混合参数重排成 flag 在前,
// 这样 `vpsbench ping http://x:8300 -d 5s` 这种直觉写法也能用
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	bools := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			bools[f.Name] = true
		}
	})
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && !bools[name] && i+1 < len(args) &&
				len(args[i+1]) > 0 && args[i+1][0] != '-' {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func targetURL(fs *flag.FlagSet) string {
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "错误: 需要且只需要一个 <url> 参数,例如 http://1.2.3.4:8300")
		os.Exit(2)
	}
	u := strings.TrimRight(rest[0], "/")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "http://" + u
	}
	return u
}

func withTok(s string) string {
	if clientToken == "" {
		return s
	}
	sep := "?"
	if strings.Contains(s, "?") {
		sep = "&"
	}
	return s + sep + "token=" + url.QueryEscape(clientToken)
}

func newHTTPClient(conns int, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        conns + 8,
		MaxIdleConnsPerHost: conns,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func getJSON(cl *http.Client, rawURL string, out any) error {
	resp, err := cl.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func hdr(title string) { fmt.Printf("\n━━━ %s ━━━\n", title) }

func kv(k string, v any) { fmt.Printf("  %-24s %v\n", k+":", v) }

func fail(err error) {
	fmt.Println("  ✗ 失败:", err)
}

// ---- ping: 延迟 / 抖动 ----

func runPingCmd(args []string) {
	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	d := fs.Duration("d", 30*time.Second, "测试时长")
	iv := fs.Duration("i", 100*time.Millisecond, "发包间隔")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	testPing(base, *d, *iv)
}

func testPing(base string, d, iv time.Duration) {
	hdr(fmt.Sprintf("延迟测试 %s(间隔 %s,共约 %.0f 个包)", d, iv, float64(d)/float64(iv)))
	cl := newHTTPClient(1, 10*time.Second)
	type rec struct {
		t  time.Time
		ms float64
	}
	var recs []rec
	errs := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", withTok(base+"/api/ping"), nil)
		resp, err := cl.Do(req)
		if err != nil {
			errs++
		} else {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			recs = append(recs, rec{t0, msF(time.Since(t0))})
		}
		cancel()
		if w := iv - time.Since(t0); w > 0 {
			time.Sleep(w)
		}
	}
	vals := make([]float64, len(recs))
	for i, r := range recs {
		vals[i] = r.ms
	}
	st := statsOf(vals)
	if st.N == 0 {
		fail(fmt.Errorf("无成功请求(错误 %d 次)", errs))
		return
	}
	kv("成功/失败", fmt.Sprintf("%d / %d", st.N, errs))
	kv("RTT p50 / p95 / p99", fmt.Sprintf("%s / %s / %s", fmtMs(st.P50), fmtMs(st.P95), fmtMs(st.P99)))
	kv("RTT 最大", fmtMs(st.Max))
	sp := append([]rec(nil), recs...)
	sort.Slice(sp, func(a, b int) bool { return sp[a].ms > sp[b].ms })
	n := 5
	if len(sp) < n {
		n = len(sp)
	}
	fmt.Println("  最差尖刺:")
	for i := 0; i < n; i++ {
		fmt.Printf("    %s  %s\n", sp[i].t.Format("15:04:05.000"), fmtMs(sp[i].ms))
	}
	if st.P99 > st.P50*3 && st.P99 > 50 {
		fmt.Println("  ⚠ p99 显著高于 p50:链路或对端存在周期性卡顿(可能是邻居偷取/网络抖动)")
	}
}

// ---- load: HTTP 并发压测 ----

func runLoadCmd(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	c := fs.Int("c", 50, "并发数")
	d := fs.Duration("d", 30*time.Second, "压测时长")
	size := fs.Int("size", 0, "响应包大小(字节,0=极小 ping 包);如 1024/2048/4096")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	res := testLoad(base, *c, *d, *size)
	printLoadResult(res, base)
}

type loadResult struct {
	Conns    int
	Size     int // 响应包字节(0 = 极小 ping 包)
	Total    int64
	Errs     int64
	Elapsed  time.Duration
	Latency  stat
	SrvSelf  float64 // 压测期间服务端自身 CPU%(均值)
	SrvSteal float64 // 压测期间服务端 steal%(均值)
}

func testLoad(base string, c int, d time.Duration, size int) loadResult {
	target := withTok(base + "/api/ping")
	if size > 0 {
		target = withTok(base + "/api/echo?size=" + strconv.Itoa(size))
	}
	cl := newHTTPClient(c, 30*time.Second)
	var total, errs atomic.Int64
	var mu sync.Mutex
	var all []float64
	var wg sync.WaitGroup
	stopAt := time.Now().Add(d)
	start := time.Now()
	for w := 0; w < c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var mine []float64
			for time.Now().Before(stopAt) {
				t0 := time.Now()
				resp, err := cl.Get(target)
				if err != nil {
					errs.Add(1)
					continue
				}
				code := resp.StatusCode
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if code != 200 {
					errs.Add(1)
					continue
				}
				total.Add(1)
				mine = append(mine, msF(time.Since(t0)))
			}
			mu.Lock()
			all = append(all, mine...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	el := time.Since(start)

	res := loadResult{Conns: c, Size: size, Total: total.Load(), Errs: errs.Load(), Elapsed: el, Latency: statsOf(all)}
	// 拉服务端视角(压测窗口内的均值)
	var snap StatsSnapshot
	sc := newHTTPClient(2, 10*time.Second)
	if err := getJSON(sc, withTok(base+"/api/stats?full=1"), &snap); err == nil && len(snap.Samples) > 0 {
		n := int(d.Seconds()) + 2
		if n > len(snap.Samples) {
			n = len(snap.Samples)
		}
		win := snap.Samples[len(snap.Samples)-n:]
		var sl, st float64
		for _, s := range win {
			sl += s.SelfCPUPct
			st += s.StealPct
		}
		res.SrvSelf = sl / float64(len(win))
		res.SrvSteal = st / float64(len(win))
	}
	return res
}

func printLoadResult(r loadResult, base string) {
	hdr(fmt.Sprintf("并发压测 %s(%d 并发 / %s)", base, r.Conns, r.Elapsed.Round(time.Millisecond)))
	kv("总请求", fmt.Sprintf("%d(%.0f req/s)", r.Total, float64(r.Total)/r.Elapsed.Seconds()))
	errRate := 0.0
	if r.Total+r.Errs > 0 {
		errRate = float64(r.Errs) / float64(r.Total+r.Errs) * 100
	}
	kv("错误", fmt.Sprintf("%d(%.2f%%)", r.Errs, errRate))
	if r.Latency.N > 0 {
		kv("延迟 p50 / p95 / p99", fmt.Sprintf("%s / %s / %s", fmtMs(r.Latency.P50), fmtMs(r.Latency.P95), fmtMs(r.Latency.P99)))
		kv("延迟最大", fmtMs(r.Latency.Max))
	}
	if r.Size > 0 && r.Total > 0 {
		mbps := float64(r.Total) * float64(r.Size) / 1e6 / r.Elapsed.Seconds()
		kv("吞吐", fmt.Sprintf("%.1f MB/s(≈ %.0f Mbps)", mbps, mbps*8))
	}
	if r.SrvSelf > 0 || r.SrvSteal > 0 {
		kv("服务端视角", fmt.Sprintf("CPU %.0f%% | steal %.1f%%", r.SrvSelf, r.SrvSteal))
	}
}

// ---- packet: 按包尺寸阶梯测并发(1K/2K/4K…) ----

func humanBytes(n int) string {
	switch {
	case n >= 1024 && n%1024 == 0:
		return fmt.Sprintf("%dK", n/1024)
	case n >= 1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func runPacketCmd(args []string) {
	fs := flag.NewFlagSet("packet", flag.ExitOnError)
	c := fs.Int("c", 100, "并发数")
	d := fs.Duration("d", 10*time.Second, "每个包尺寸的测试时长")
	sizes := fs.String("sizes", "1024,2048,4096", "逗号分隔的包大小(字节,≤65536)")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok

	var ss []int
	for _, p := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err == nil && n > 0 && n <= 65536 {
			ss = append(ss, n)
		}
	}
	if len(ss) == 0 {
		fmt.Fprintln(os.Stderr, "错误: sizes 参数无效,例如 -sizes 1024,2048,4096")
		os.Exit(2)
	}
	hdr(fmt.Sprintf("包尺寸并发阶梯(%d 并发 × %s/档)", *c, d))
	fmt.Printf("  %-6s %12s %14s %10s %10s %10s %8s\n", "包", "RPS", "吞吐", "p50", "p99", "最大", "错误%")
	var first, last float64
	for i, n := range ss {
		fmt.Printf("  %-6s ", humanBytes(n))
		r := testLoad(base, *c, *d, n)
		rps := float64(r.Total) / r.Elapsed.Seconds()
		mbps := float64(r.Total) * float64(n) / 1e6 / r.Elapsed.Seconds()
		er := 0.0
		if r.Total+r.Errs > 0 {
			er = float64(r.Errs) / float64(r.Total+r.Errs) * 100
		}
		if r.Latency.N == 0 {
			fmt.Printf("%12s %14s   —— 全部失败 ——\n", "-", "-")
			continue
		}
		fmt.Printf("%12.0f %10.1fMB/s %10s %10s %10s %7.2f%%\n",
			rps, mbps, fmtMs(r.Latency.P50), fmtMs(r.Latency.P99), fmtMs(r.Latency.Max), er)
		if i == 0 {
			first = rps
		}
		last = rps
	}
	if first > 0 && last > 0 {
		fmt.Println()
		switch {
		case last < first*0.35:
			fmt.Printf("  分析: RPS 随包增大急剧下降(%.0f→%.0f)——带宽/内核缓冲瓶颈,大包场景看吞吐而非 RPS\n", first, last)
		case last > first*0.8:
			fmt.Printf("  分析: RPS 基本不随包大小变化(%.0f→%.0f)——请求处理/CPU 瓶颈,包尺寸不是短板\n", first, last)
		default:
			fmt.Printf("  分析: RPS 中等下降(%.0f→%.0f)——带宽与请求处理共同作用\n", first, last)
		}
	}
}

// ---- conn: 最大并发连接数 ----

func runConnCmd(args []string) {
	fs := flag.NewFlagSet("conn", flag.ExitOnError)
	c := fs.Int("c", 500, "目标并发连接数")
	hold := fs.Duration("hold", 30*time.Second, "每个连接保持时长")
	step := fs.Int("step", 50, "每波新增连接数")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok

	hdr(fmt.Sprintf("最大并发连接测试(目标 %d,每波 +%d,保持 %s)", *c, *step, *hold))
	cl := newHTTPClient(*c+16, 30*time.Second)
	ctxAll, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	var ok, failN atomic.Int64
	var wg sync.WaitGroup
	sec := int(hold.Seconds())
	opened := 0
	for opened < *c {
		wave := *step
		if opened+wave > *c {
			wave = *c - opened
		}
		for j := 0; j < wave; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(ctxAll, *hold+60*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, "GET", withTok(base+"/api/hold?sec="+strconv.Itoa(sec)), nil)
				resp, err := cl.Do(req)
				if err != nil {
					failN.Add(1)
					return
				}
				ok.Add(1) // 响应头已到:连接建立并被服务端保持
				<-ctxAll.Done()
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}()
		}
		opened += wave
		fmt.Printf("  已发起 %4d,当前成功 %d,失败 %d\n", opened, ok.Load(), failN.Load())
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("  全部发起完毕,保持 %s 后释放...\n", *hold)
	wg.Wait()
	kv("成功建立", ok.Load())
	kv("失败", failN.Load())
	if ok.Load() >= int64(*c) {
		fmt.Println("  ✓ 达到目标并发数,可加大 -c 继续探测上限")
	} else {
		fmt.Println("  ✗ 未达目标:可能受 conntrack / 文件描述符 / 防火墙限制")
	}
}

// ---- bw: 带宽 ----

func runBWCmd(args []string) {
	fs := flag.NewFlagSet("bw", flag.ExitOnError)
	d := fs.Duration("d", 10*time.Second, "测试时长")
	c := fs.Int("c", 4, "并行流数")
	up := fs.Bool("up", false, "测上行(默认测下行)")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	testBW(base, *d, *c, *up)
}

func testBW(base string, d time.Duration, c int, up bool) float64 {
	dir := "下行"
	if up {
		dir = "上行"
	}
	hdr(fmt.Sprintf("%s带宽测试(%d 并行流 / %s)", dir, c, d))
	cl := newHTTPClient(c, 60*time.Second)
	var total atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(d)
	if !up {
		for w := 0; w < c; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					resp, err := cl.Get(withTok(base + "/api/stream?mb=50"))
					if err != nil {
						continue
					}
					n, _ := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					total.Add(n)
				}
			}()
		}
	} else {
		payload := randBuf(8 << 20)
		for w := 0; w < c; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					resp, err := cl.Post(withTok(base+"/api/upload"), "application/octet-stream", bytes.NewReader(payload))
					if err != nil {
						continue
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					total.Add(int64(len(payload)))
				}
			}()
		}
	}
	wg.Wait()
	mbps := float64(total.Load()) / 1e6 / d.Seconds()
	kv("总流量", fmt.Sprintf("%.1f MB", float64(total.Load())/1e6))
	kv("吞吐", fmt.Sprintf("%.1f MB/s(≈ %.0f Mbps)", mbps, mbps*8))
	return mbps
}

// ---- cpu / fill / disk ----

func runCPUCmd(args []string) {
	fs := flag.NewFlagSet("cpu", flag.ExitOnError)
	ms := fs.Int("ms", 2000, "每轮时长")
	runs := fs.Int("runs", 3, "轮数,取最优")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	testCPU(base, *ms, *runs)
}

func testCPU(base string, ms, runs int) float64 {
	hdr(fmt.Sprintf("单核算力测试(%d 轮 × %dms,取最优)", runs, ms))
	cl := newHTTPClient(2, 2*time.Minute)
	best := 0.0
	for i := 1; i <= runs; i++ {
		var cr cpuResp
		if err := getJSON(cl, withTok(base+"/api/cpu?ms="+strconv.Itoa(ms)), &cr); err != nil {
			fail(err)
			return 0
		}
		fmt.Printf("  第 %d 轮: %.2f M ops/s(墙钟 %s)\n", i, cr.OpsPerSec/1e6, fmtMs(cr.Ms))
		if cr.OpsPerSec > best {
			best = cr.OpsPerSec
		}
	}
	kv("单核算力", fmt.Sprintf("%.2f M ops/s", best/1e6))
	return best
}

func runFillCmd(args []string) {
	fs := flag.NewFlagSet("fill", flag.ExitOnError)
	n := fs.Int("n", 0, "打满核数(0=服务端全部核)")
	ms := fs.Int("ms", 5000, "持续时长")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	testFill(base, *n, *ms, 0)
}

// singleCoreOps 传入时用于对比"单核空载 vs 满载"的每核算力衰减
func testFill(base string, n, ms int, singleCoreOps float64) *fillResp {
	cl := newHTTPClient(4, 5*time.Minute)
	var info infoResp
	if err := getJSON(cl, withTok(base+"/api/info"), &info); err != nil {
		fail(err)
		return nil
	}
	if n <= 0 {
		n = info.NumCPU
	}
	hdr(fmt.Sprintf("满载测试:打满 %d 核 × %dms(服务端标称 %d 核)", n, ms, info.NumCPU))
	fmt.Printf("  运行中...\n")
	var fr fillResp
	if err := getJSON(cl, withTok(base+fmt.Sprintf("/api/fill?n=%d&ms=%d", n, ms)), &fr); err != nil {
		fail(err)
		return nil
	}
	ratio := fr.EffectiveCores / float64(n)
	kv("实际拿到", fmt.Sprintf("%.2f 核 × %.1fs = %.2f CPU 秒", fr.EffectiveCores, fr.Ms/1000, fr.CPUSec))
	kv("核数兑现率", fmt.Sprintf("%.1f%%(%d 核里实际可用 %.2f 核)", ratio*100, n, fr.EffectiveCores))
	if singleCoreOps > 0 && fr.OpsPerCorePerSec > 0 {
		decay := fr.OpsPerCorePerSec / singleCoreOps * 100
		kv("满载单核衰减", fmt.Sprintf("%.0f%%(空载 %.1f M/s → 满载 %.1f M/s)", decay, singleCoreOps/1e6, fr.OpsPerCorePerSec/1e6))
	}
	switch {
	case ratio >= 0.95:
		fmt.Println("  ✓ 核数真实:基本独享,无超售/限速")
	case ratio >= 0.85:
		fmt.Println("  △ 轻度争抢:有邻居或 cgroup 配额略紧")
	case ratio >= 0.7:
		fmt.Println("  ⚠ 明显超售:标称核数兑现不足 85%")
	default:
		fmt.Println("  ✗ 严重超售或强限速:兑现不足 70%,共享宿主机无疑")
	}
	return &fr
}

func runDiskCmd(args []string) {
	fs := flag.NewFlagSet("disk", flag.ExitOnError)
	mb := fs.Int("mb", 64, "顺序读写 MB")
	files := fs.Int("files", 100, "小文件个数(fsync 延迟样本)")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	testDisk(base, *mb, *files)
}

func testDisk(base string, mb, files int) *diskResp {
	hdr(fmt.Sprintf("磁盘测试(顺序 %d MB + %d 个小文件 fsync)", mb, files))
	cl := newHTTPClient(2, 5*time.Minute)
	var dr diskResp
	if err := getJSON(cl, withTok(base+fmt.Sprintf("/api/disk?mb=%d&files=%d", mb, files)), &dr); err != nil {
		fail(err)
		return nil
	}
	kv("顺序写", fmt.Sprintf("%s(%.0f MB/s)", fmtMs(dr.WriteMs), dr.WriteMBps))
	kv("大文件 fsync", fmtMs(dr.FsyncMs))
	kv("顺序读(含缓存)", fmt.Sprintf("%s(%.0f MB/s)", fmtMs(dr.ReadMs), dr.ReadMBps))
	kv("小文件 fsync p50/p95/p99", fmt.Sprintf("%s / %s / %s", fmtMs(dr.Fsync.P50), fmtMs(dr.Fsync.P95), fmtMs(dr.Fsync.P99)))
	kv("小文件 fsync 最大", fmtMs(dr.Fsync.Max))
	switch {
	case dr.Fsync.P99 < 1:
		fmt.Println("  ✓ 存储优秀(本地 NVMe 级)")
	case dr.Fsync.P99 < 3:
		fmt.Println("  ✓ 存储良好")
	case dr.Fsync.P99 < 10:
		fmt.Println("  △ 存储一般:网络存储或邻居 I/O 争抢")
	default:
		fmt.Println("  ⚠ 存储延迟高:严重 I/O 争抢或超售,建库/写日志会痛")
	}
	return &dr
}

// ---- steal: 服务端偷取报告 ----

func runStealCmd(args []string) {
	fs := flag.NewFlagSet("steal", flag.ExitOnError)
	watch := fs.Bool("w", false, "持续观察模式,每 5s 刷新一行")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	cl := newHTTPClient(2, 15*time.Second)
	if !*watch {
		var s StatsSnapshot
		if err := getJSON(cl, withTok(base+"/api/stats"), &s); err != nil {
			fail(err)
			return
		}
		printSnapshot(&s, true)
		return
	}
	fmt.Println("持续观察中(Ctrl+C 退出)...")
	for {
		var s StatsSnapshot
		if err := getJSON(cl, withTok(base+"/api/stats"), &s); err != nil {
			fmt.Println("  拉取失败:", err)
		} else {
			fmt.Printf("[%s] steal: cur %.1f%% avg %.1f%% max %.1f%% | 抖动 p99 %s max %s | 负载 %.2f\n",
				time.Now().Format("15:04:05"), s.StealCur, s.StealAvg, s.StealMax,
				fmtMs(s.JitP99), fmtMs(s.JitMax), s.Load[0])
		}
		time.Sleep(5 * time.Second)
	}
}

func printSnapshot(s *StatsSnapshot, detail bool) {
	hdr("服务端资源偷取报告(共享宿主机检测)")
	kv("主机", fmt.Sprintf("%s | %d 核 | 监控已运行 %s", s.Hostname, s.NumCPU, (time.Duration(s.UptimeSec * float64(time.Second))).Round(time.Second)))
	kv("内存", fmt.Sprintf("%d MB 可用 / %d MB", s.MemAvailMB, s.MemTotalMB))
	kv("负载(1/5/15m)", fmt.Sprintf("%.2f / %.2f / %.2f", s.Load[0], s.Load[1], s.Load[2]))
	fmt.Println()
	kv("CPU steal%", fmt.Sprintf("当前 %.1f | 平均 %.1f | p95 %.1f | 最大 %.1f", s.StealCur, s.StealAvg, s.StealP95, s.StealMax))
	kv(fmt.Sprintf("调度抖动(基准 %dms)", s.JitInterval), fmt.Sprintf("p50 %s | p95 %s | p99 %s | 最大 %s",
		fmtMs(s.JitP50), fmtMs(s.JitP95), fmtMs(s.JitP99), fmtMs(s.JitMax)))
	kv("抖动超限次数", fmt.Sprintf(">10ms: %d | >50ms: %d | >100ms: %d", s.JitOver10, s.JitOver50, s.JitOver100))
	if s.StealMax == 0 && s.StealAvg == 0 && s.StealP95 == 0 && s.SampleCount > 60 {
		fmt.Println("  ℹ steal 恒为 0:部分虚拟化(如 OpenVZ/LXC)看不到 steal,请以调度抖动为准")
	}
	if detail && len(s.Spikes) > 0 {
		fmt.Println("  偷取事件(最近):")
		start := 0
		if len(s.Spikes) > 8 {
			start = len(s.Spikes) - 8
		}
		for _, e := range s.Spikes[start:] {
			if e.Kind == "steal" {
				fmt.Printf("    %s  steal %.1f%%\n", e.T.Format("01-02 15:04:05"), e.StealPct)
			} else {
				fmt.Printf("    %s  抖动 %s\n", e.T.Format("01-02 15:04:05"), fmtMs(e.JitterMs))
			}
		}
	}
	if s.SelfBusy() {
		fmt.Println("  ℹ 近期服务端自身 CPU 较高(可能刚跑过压测),抖动/负载数据会偏高,空闲时复测更准")
	}
	verdictSteal(s)
}

func (s *StatsSnapshot) SelfBusy() bool {
	if len(s.Samples) == 0 {
		return false
	}
	n := 30
	if len(s.Samples) < n {
		n = len(s.Samples)
	}
	var sum float64
	for _, x := range s.Samples[len(s.Samples)-n:] {
		sum += x.SelfCPUPct
	}
	return sum/float64(n) > 50
}

func verdictSteal(s *StatsSnapshot) {
	fmt.Println()
	switch {
	case s.StealAvg < 1 && s.StealMax < 10 && s.JitP99 < 10:
		fmt.Println("  结论 ✓ 空闲期无偷取迹象:宿主机不挤(建议连续观察 24h 复核高峰时段)")
	case s.StealAvg < 3 && s.StealMax < 25 && s.JitP99 < 30:
		fmt.Println("  结论 △ 轻度争抢:有邻居但影响可控")
	default:
		fmt.Println("  结论 ⚠ 偷取明显:宿主机超售严重,延迟敏感业务慎选")
	}
}

// ---- all: 全套 ----

func runAllCmd(args []string) {
	fs := flag.NewFlagSet("all", flag.ExitOnError)
	c := fs.Int("c", 50, "并发压测连接数")
	ld := fs.Duration("ld", 20*time.Second, "压测时长")
	pd := fs.Duration("pd", 15*time.Second, "延迟测试时长")
	bd := fs.Duration("bd", 8*time.Second, "带宽测试时长")
	fd := fs.Int("fill-ms", 8000, "满载测试时长")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok

	cl := newHTTPClient(4, 15*time.Second)
	var info infoResp
	if err := getJSON(cl, withTok(base+"/api/info"), &info); err != nil {
		fmt.Println("✗ 连接服务端失败:", err)
		fmt.Println("  检查:服务端是否启动、端口是否放行(ufw allow 8300)、-token 是否匹配")
		os.Exit(1)
	}
	fmt.Printf("╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║        vpsbench 全套测试 → %s\n", base)
	fmt.Printf("╚══════════════════════════════════════════════╝\n")
	hdr("被测机器信息")
	kv("主机名", info.Hostname)
	kv("CPU", fmt.Sprintf("%d 核", info.NumCPU))
	kv("内存", fmt.Sprintf("%d MB / %d MB", info.MemAvailMB, info.MemTotalMB))
	kv("内核", info.Kernel)
	kv("开机时长", (time.Duration(info.UptimeSec * float64(time.Second))).Round(time.Minute))

	single := testCPU(base, 1000, 2)
	fr := testFill(base, 0, *fd, single)
	dr := testDisk(base, 64, 100)
	testPing(base, *pd, 100*time.Millisecond)
	res := testLoad(base, *c, *ld, 0)
	printLoadResult(res, base)
	bw := testBW(base, *bd, 4, false)

	var snap StatsSnapshot
	if err := getJSON(cl, withTok(base+"/api/stats"), &snap); err == nil {
		printSnapshot(&snap, true)
	}

	// ── All-in-One 服务器信息汇总(整段可直接复制) ──
	fmt.Println()
	line := strings.Repeat("─", 52)
	fmt.Println("┌" + line + "┐")
	fmt.Println("│ 服务器信息汇总 All-in-One" + strings.Repeat(" ", 25) + "│")
	fmt.Println("└" + line + "┘")
	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "?"
	}
	fmt.Printf("  主机       : %s | %d 核 | %d/%d MB 内存\n", info.Hostname, info.NumCPU, info.MemAvailMB, info.MemTotalMB)
	fmt.Printf("  系统       : %s | 开机 %s\n", info.Kernel, (time.Duration(info.UptimeSec * float64(time.Second))).Round(time.Minute))
	fmt.Printf("  负载       : %.2f / %.2f / %.2f\n", snap.Load[0], snap.Load[1], snap.Load[2])
	fmt.Printf("  CPU 算力   : 单核 %.2f M ops/s %s\n", single/1e6, mark(single > 0))
	if fr != nil {
		ratio := fr.EffectiveCores / float64(fr.ReqCores) * 100
		fmt.Printf("  核数兑现   : %d 核实际 %.2f 核(%.0f%%) %s\n", fr.ReqCores, fr.EffectiveCores, ratio,
			map[bool]string{true: "✓", false: "⚠"}[ratio >= 85])
	}
	if dr != nil {
		fmt.Printf("  磁盘       : 写 %.0f MB/s | 小文件 fsync p99 %s %s\n", dr.WriteMBps, fmtMs(dr.Fsync.P99),
			map[bool]string{true: "✓", false: "⚠"}[dr.Fsync.P99 < 10])
	}
	fmt.Printf("  偷取 steal : 平均 %.1f%% | 峰值 %.1f%% %s\n", snap.StealAvg, snap.StealMax,
		mark(snap.StealAvg < 3 && snap.StealMax < 25))
	fmt.Printf("  调度抖动   : p99 %s | 峰值 %s %s\n", fmtMs(snap.JitP99), fmtMs(snap.JitMax),
		mark(snap.JitP99 < 30))
	if res.Latency.N > 0 {
		fmt.Printf("  网络       : RTT p50 %s | p99 %s\n", fmtMs(res.Latency.P50), fmtMs(res.Latency.P99))
		errRate := float64(res.Errs) / float64(res.Total+res.Errs) * 100
		fmt.Printf("  并发       : %.0f req/s @ %d 并发 | 错误 %.2f%% %s\n",
			float64(res.Total)/res.Elapsed.Seconds(), res.Conns, errRate, mark(errRate < 1))
	}
	fmt.Printf("  下行带宽   : %.1f MB/s(≈%.0f Mbps)%s\n", bw, bw*8, mark(bw > 10))
	fmt.Println()
	fmt.Println("  建议: `vpsbench steal <url> -w` 挂 24h 抓高峰偷取;")
	fmt.Println("        `vpsbench packet <url> -c 100` 测 1K/2K/4K 包并发;")
	fmt.Println("        `vpsbench conn <url> -c 1000` 探最大并发连接数。")
}
