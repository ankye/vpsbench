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
		fmt.Fprintln(os.Stderr, T("Error: exactly one <url> argument is required, e.g. http://1.2.3.4:8300"))
		os.Exit(2)
	}
	u := strings.TrimRight(rest[0], "/")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "http://" + u
	}
	return u
}

var embeddedSrvs []*http.Server

// startEmbedded 在本进程内起一个仅监听回环的临时服务,用于免 server 自测
func startEmbedded() string {
	if diskDir == "" {
		diskDir = os.TempDir()
	}
	srv := &http.Server{Handler: newRouter(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, T("✗ cannot start embedded server:"), err)
		os.Exit(1)
	}
	go srv.Serve(ln)
	embeddedSrvs = append(embeddedSrvs, srv)
	return "http://" + ln.Addr().String()
}

// resolveTargetURL:URL 省略 ⇒ 进程内自测(不开外部端口);给了 URL ⇒ 作为远程客户端
func resolveTargetURL(fs *flag.FlagSet) string {
	if fs.NArg() == 0 {
		base := startEmbedded()
		fmt.Println("ℹ " + T("no URL given — testing this machine via an in-process server (loopback)"))
		return base
	}
	return targetURL(fs)
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

func kv(k string, v any) { fmt.Printf("  %-28s %v\n", T(k)+":", v) }

func fail(err error) {
	fmt.Println("  "+T("✗ failed:"), err)
}

// ---- ping: 延迟 / 抖动 ----

func runPingCmd(args []string) {
	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	d := fs.Duration("d", 30*time.Second, T("test duration"))
	iv := fs.Duration("i", 100*time.Millisecond, T("send interval"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok
	testPing(base, *d, *iv)
}

func testPing(base string, d, iv time.Duration) {
	hdr(TF("Latency test %s (interval %s, ~%.0f probes)", d, iv, float64(d)/float64(iv)))
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
		fail(fmt.Errorf(T("no successful requests (%d errors)"), errs))
		return
	}
	kv("ok / failed", fmt.Sprintf("%d / %d", st.N, errs))
	kv("RTT p50 / p95 / p99", fmt.Sprintf("%s / %s / %s", fmtMs(st.P50), fmtMs(st.P95), fmtMs(st.P99)))
	kv("RTT max", fmtMs(st.Max))
	sp := append([]rec(nil), recs...)
	sort.Slice(sp, func(a, b int) bool { return sp[a].ms > sp[b].ms })
	n := 5
	if len(sp) < n {
		n = len(sp)
	}
	fmt.Println("  " + T("worst spikes:"))
	for i := 0; i < n; i++ {
		fmt.Printf("    %s  %s\n", sp[i].t.Format("15:04:05.000"), fmtMs(sp[i].ms))
	}
	if st.P99 > st.P50*3 && st.P99 > 50 {
		fmt.Println("  " + T("⚠ p99 ≫ p50: periodic stalls on path or peer (neighbor steal / jitter)"))
	}
}

// ---- load: HTTP 并发压测 ----

func runLoadCmd(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	c := fs.Int("c", 50, T("concurrency"))
	d := fs.Duration("d", 30*time.Second, T("test duration"))
	size := fs.Int("size", 0, T("response packet size in bytes (0=tiny ping), e.g. 1024/2048/4096"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
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
	hdr(TF("Load test %s (%d conns / %s)", base, r.Conns, r.Elapsed.Round(time.Millisecond)))
	kv("total requests", fmt.Sprintf("%d (%.0f req/s)", r.Total, float64(r.Total)/r.Elapsed.Seconds()))
	errRate := 0.0
	if r.Total+r.Errs > 0 {
		errRate = float64(r.Errs) / float64(r.Total+r.Errs) * 100
	}
	kv("errors", fmt.Sprintf("%d (%.2f%%)", r.Errs, errRate))
	if r.Latency.N > 0 {
		kv("latency p50 / p95 / p99", fmt.Sprintf("%s / %s / %s", fmtMs(r.Latency.P50), fmtMs(r.Latency.P95), fmtMs(r.Latency.P99)))
		kv("latency max", fmtMs(r.Latency.Max))
	}
	if r.Size > 0 && r.Total > 0 {
		mbps := float64(r.Total) * float64(r.Size) / 1e6 / r.Elapsed.Seconds()
		kv("throughput", TF("%.1f MB/s (≈ %.0f Mbps)", mbps, mbps*8))
	}
	if r.SrvSelf > 0 || r.SrvSteal > 0 {
		kv("server view", fmt.Sprintf("CPU %.0f%% | steal %.1f%%", r.SrvSelf, r.SrvSteal))
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
	c := fs.Int("c", 100, T("concurrency"))
	d := fs.Duration("d", 10*time.Second, T("duration per packet size"))
	sizes := fs.String("sizes", "1024,2048,4096", T("comma-separated packet sizes in bytes (≤65536)"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok

	var ss []int
	for _, p := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err == nil && n > 0 && n <= 65536 {
			ss = append(ss, n)
		}
	}
	if len(ss) == 0 {
		fmt.Fprintln(os.Stderr, T("Error: invalid sizes, e.g. -sizes 1024,2048,4096"))
		os.Exit(2)
	}
	hdr(TF("Packet-size concurrency ladder (%d conns × %s each)", *c, d))
	fmt.Printf("  %-6s %12s %14s %10s %10s %10s %8s\n",
		T("pkt"), "RPS", T("throughput"), "p50", "p99", T("max"), T("err%"))
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
			fmt.Printf("%12s %14s   %s\n", "-", "-", T("—— all failed ——"))
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
			fmt.Println("  " + TF("Analysis: RPS drops sharply with size (%.0f→%.0f) — bandwidth/kernel-buffer bound; watch throughput for big packets, not RPS", first, last))
		case last > first*0.8:
			fmt.Println("  " + TF("Analysis: RPS barely changes with size (%.0f→%.0f) — request-processing/CPU bound; packet size is not the bottleneck", first, last))
		default:
			fmt.Println("  " + TF("Analysis: RPS drops moderately (%.0f→%.0f) — bandwidth and processing both contribute", first, last))
		}
	}
}

// ---- conn: 最大并发连接数 ----

func runConnCmd(args []string) {
	fs := flag.NewFlagSet("conn", flag.ExitOnError)
	c := fs.Int("c", 500, T("target concurrent connections"))
	hold := fs.Duration("hold", 30*time.Second, T("hold time per connection"))
	step := fs.Int("step", 50, T("new connections per wave"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok

	hdr(TF("Max concurrent connections (target %d, +%d per wave, hold %s)", *c, *step, *hold))
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
		fmt.Print(TF("  launched %4d, ok %d, failed %d\n", opened, ok.Load(), failN.Load()))
		time.Sleep(2 * time.Second)
	}
	fmt.Println("  " + TF("all launched, holding %s then releasing...", *hold))
	wg.Wait()
	kv("established", ok.Load())
	kv("failed", failN.Load())
	if ok.Load() >= int64(*c) {
		fmt.Println("  " + T("✓ reached target; raise -c to probe the ceiling"))
	} else {
		fmt.Println("  " + T("✗ below target: conntrack / fd limit / firewall?"))
	}
}

// ---- bw: 带宽 ----

func runBWCmd(args []string) {
	fs := flag.NewFlagSet("bw", flag.ExitOnError)
	d := fs.Duration("d", 10*time.Second, T("test duration"))
	c := fs.Int("c", 4, T("parallel streams"))
	up := fs.Bool("up", false, T("test upload (default: download)"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok
	testBW(base, *d, *c, *up)
}

func testBW(base string, d time.Duration, c int, up bool) float64 {
	dir := T("Download")
	if up {
		dir = T("Upload")
	}
	hdr(TF("%s bandwidth test (%d streams / %s)", dir, c, d))
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
	kv("total transferred", TF("%.1f MB", float64(total.Load())/1e6))
	kv("throughput", TF("%.1f MB/s (≈ %.0f Mbps)", mbps, mbps*8))
	return mbps
}

// ---- cpu / fill / disk ----

func runCPUCmd(args []string) {
	fs := flag.NewFlagSet("cpu", flag.ExitOnError)
	ms := fs.Int("ms", 2000, T("ms per run"))
	runs := fs.Int("runs", 3, T("runs, keep best"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok
	testCPU(base, *ms, *runs)
}

func testCPU(base string, ms, runs int) float64 {
	hdr(TF("Single-core benchmark (%d runs × %dms, best)", runs, ms))
	cl := newHTTPClient(2, 2*time.Minute)
	best := 0.0
	for i := 1; i <= runs; i++ {
		var cr cpuResp
		if err := getJSON(cl, withTok(base+"/api/cpu?ms="+strconv.Itoa(ms)), &cr); err != nil {
			fail(err)
			return 0
		}
		fmt.Print(TF("  run #%d: %.2f M ops/s (wall %s)\n", i, cr.OpsPerSec/1e6, fmtMs(cr.Ms)))
		if cr.OpsPerSec > best {
			best = cr.OpsPerSec
		}
	}
	kv("single-core", TF("%.2f M ops/s", best/1e6))
	return best
}

func runFillCmd(args []string) {
	fs := flag.NewFlagSet("fill", flag.ExitOnError)
	n := fs.Int("n", 0, T("cores to fill (0=all)"))
	ms := fs.Int("ms", 5000, T("duration in ms"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
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
	hdr(TF("Full-load test: fill %d cores × %dms (server has %d cores)", n, ms, info.NumCPU))
	fmt.Print("  " + T("  running...\n"))
	var fr fillResp
	if err := getJSON(cl, withTok(base+fmt.Sprintf("/api/fill?n=%d&ms=%d", n, ms)), &fr); err != nil {
		fail(err)
		return nil
	}
	ratio := fr.EffectiveCores / float64(n)
	kv("actually got", TF("%.2f cores × %.1fs = %.2f CPU-seconds", fr.EffectiveCores, fr.Ms/1000, fr.CPUSec))
	kv("core delivery", TF("%.1f%% (%.2f of %d cores actually usable)", ratio*100, fr.EffectiveCores, n))
	if singleCoreOps > 0 && fr.OpsPerCorePerSec > 0 {
		decay := fr.OpsPerCorePerSec / singleCoreOps * 100
		kv("per-core decay at full load", TF("%.0f%% (idle %.1f M/s → loaded %.1f M/s)", decay, singleCoreOps/1e6, fr.OpsPerCorePerSec/1e6))
	}
	switch {
	case ratio >= 0.95:
		fmt.Println("  " + T("✓ real cores: basically dedicated, no oversell/throttle"))
	case ratio >= 0.85:
		fmt.Println("  " + T("△ mild contention: neighbors or tight cgroup quota"))
	case ratio >= 0.7:
		fmt.Println("  " + T("⚠ clear oversell: less than 85% of advertised cores"))
	default:
		fmt.Println("  " + T("✗ severe oversell or hard throttle: <70%, definitely shared host"))
	}
	return &fr
}

func runDiskCmd(args []string) {
	fs := flag.NewFlagSet("disk", flag.ExitOnError)
	mb := fs.Int("mb", 64, T("sequential MB"))
	files := fs.Int("files", 100, T("small file count (fsync samples)"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok
	testDisk(base, *mb, *files)
}

func testDisk(base string, mb, files int) *diskResp {
	hdr(TF("Disk test (seq %d MB + %d small-file fsync)", mb, files))
	cl := newHTTPClient(2, 5*time.Minute)
	var dr diskResp
	if err := getJSON(cl, withTok(base+fmt.Sprintf("/api/disk?mb=%d&files=%d", mb, files)), &dr); err != nil {
		fail(err)
		return nil
	}
	kv("sequential write", TF("%s (%.0f MB/s)", fmtMs(dr.WriteMs), dr.WriteMBps))
	kv("large-file fsync", fmtMs(dr.FsyncMs))
	kv("sequential read (cached)", TF("%s (%.0f MB/s)", fmtMs(dr.ReadMs), dr.ReadMBps))
	kv("small-file fsync p50/p95/p99", fmt.Sprintf("%s / %s / %s", fmtMs(dr.Fsync.P50), fmtMs(dr.Fsync.P95), fmtMs(dr.Fsync.P99)))
	kv("small-file fsync max", fmtMs(dr.Fsync.Max))
	switch {
	case dr.Fsync.P99 < 1:
		fmt.Println("  " + T("✓ excellent storage (local NVMe class)"))
	case dr.Fsync.P99 < 3:
		fmt.Println("  " + T("✓ good storage"))
	case dr.Fsync.P99 < 10:
		fmt.Println("  " + T("△ mediocre: network storage or neighbor I/O contention"))
	default:
		fmt.Println("  " + T("⚠ high storage latency: heavy I/O contention or oversell — painful for DB/logs"))
	}
	return &dr
}

// ---- steal: 服务端偷取报告 ----

func runStealCmd(args []string) {
	fs := flag.NewFlagSet("steal", flag.ExitOnError)
	watch := fs.Bool("w", false, T("watch mode, refresh every 5s"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
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
	fmt.Println(T("watching... (Ctrl+C to quit)"))
	for {
		var s StatsSnapshot
		if err := getJSON(cl, withTok(base+"/api/stats"), &s); err != nil {
			fmt.Println(T("  fetch failed:"), err)
		} else {
			fmt.Print(TF("[%s] steal: cur %.1f%% avg %.1f%% max %.1f%% | jitter p99 %s max %s | load %.2f\n",
				time.Now().Format("15:04:05"), s.StealCur, s.StealAvg, s.StealMax,
				fmtMs(s.JitP99), fmtMs(s.JitMax), s.Load[0]))
		}
		time.Sleep(5 * time.Second)
	}
}

func printSnapshot(s *StatsSnapshot, detail bool) {
	hdr(T("Server steal report (shared-host detection)"))
	kv("host", TF("%s | %d cores | monitoring for %s", s.Hostname, s.NumCPU,
		(time.Duration(s.UptimeSec*float64(time.Second))).Round(time.Second)))
	kv("memory", TF("%d MB avail / %d MB", s.MemAvailMB, s.MemTotalMB))
	kv("load (1/5/15m)", fmt.Sprintf("%.2f / %.2f / %.2f", s.Load[0], s.Load[1], s.Load[2]))
	fmt.Println()
	kv("CPU steal%", TF("cur %.1f | avg %.1f | p95 %.1f | max %.1f", s.StealCur, s.StealAvg, s.StealP95, s.StealMax))
	kv(TF("sched jitter (base %dms)", s.JitInterval), TF("p50 %s | p95 %s | p99 %s | max %s",
		fmtMs(s.JitP50), fmtMs(s.JitP95), fmtMs(s.JitP99), fmtMs(s.JitMax)))
	kv("jitter over-limit count", TF(">10ms: %d | >50ms: %d | >100ms: %d", s.JitOver10, s.JitOver50, s.JitOver100))
	if s.StealMax == 0 && s.StealAvg == 0 && s.StealP95 == 0 && s.SampleCount > 60 {
		fmt.Println("  " + T("ℹ steal always 0: some virtualization (OpenVZ/LXC) hides steal — judge by scheduling jitter"))
	}
	if detail && len(s.Spikes) > 0 {
		fmt.Println("  " + T("steal/jitter events (recent):"))
		start := 0
		if len(s.Spikes) > 8 {
			start = len(s.Spikes) - 8
		}
		for _, e := range s.Spikes[start:] {
			if e.Kind == "steal" {
				fmt.Printf("    %s  steal %.1f%%\n", e.T.Format("01-02 15:04:05"), e.StealPct)
			} else {
				fmt.Print(TF("    %s  jitter %s\n", e.T.Format("01-02 15:04:05"), fmtMs(e.JitterMs)))
			}
		}
	}
	if s.SelfBusy() {
		fmt.Println("  " + T("ℹ server's own CPU was busy recently (load test?): jitter/load inflated, retest when idle"))
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
		fmt.Println("  " + T("Verdict ✓ no steal while idle: host not crowded (watch 24h to confirm peak hours)"))
	case s.StealAvg < 3 && s.StealMax < 25 && s.JitP99 < 30:
		fmt.Println("  " + T("Verdict △ mild contention: neighbors exist but impact is contained"))
	default:
		fmt.Println("  " + T("Verdict ⚠ obvious steal: heavily oversold host — think twice for latency-sensitive work"))
	}
}

// ---- all: 全套 ----

func runAllCmd(args []string) {
	fs := flag.NewFlagSet("all", flag.ExitOnError)
	c := fs.Int("c", 50, T("load-test connections"))
	ld := fs.Duration("ld", 20*time.Second, T("load-test duration"))
	pd := fs.Duration("pd", 15*time.Second, T("latency-test duration"))
	bd := fs.Duration("bd", 8*time.Second, T("bandwidth-test duration"))
	fd := fs.Int("fill-ms", 8000, T("full-load test duration in ms"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := resolveTargetURL(fs)
	clientToken = *tok

	cl := newHTTPClient(4, 15*time.Second)
	var info infoResp
	if err := getJSON(cl, withTok(base+"/api/info"), &info); err != nil {
		fmt.Println(TF("✗ cannot reach server: %v", err))
		fmt.Println(T("  check: server running? port open (ufw allow 8300)? -token match?"))
		os.Exit(1)
	}
	fmt.Printf("╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║        vpsbench full suite → %s\n", base)
	fmt.Printf("╚══════════════════════════════════════════════╝\n")
	hdr(T("target machine info"))
	kv("hostname", info.Hostname)
	kv("CPU", TF("%d cores", info.NumCPU))
	kv("memory", fmt.Sprintf("%d MB / %d MB", info.MemAvailMB, info.MemTotalMB))
	kv("kernel", info.Kernel)
	kv("uptime", (time.Duration(info.UptimeSec * float64(time.Second))).Round(time.Minute))

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
	fmt.Println("│  " + T("Server summary All-in-One") + strings.Repeat(" ", 24) + "│")
	fmt.Println("└" + line + "┘")
	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "?"
	}
	fmt.Printf("  %-12s: %s | %s | %d/%d MB\n", T("host"), info.Hostname, TF("%d cores", info.NumCPU), info.MemAvailMB, info.MemTotalMB)
	fmt.Printf("  %-12s: %s | %s %s\n", T("system"), info.Kernel, T("uptime"),
		(time.Duration(info.UptimeSec * float64(time.Second))).Round(time.Minute))
	fmt.Printf("  %-12s: %.2f / %.2f / %.2f\n", T("load (1/5/15m)"), snap.Load[0], snap.Load[1], snap.Load[2])
	fmt.Print(TF("single-core %.2f M ops/s %s\n", single/1e6, mark(single > 0)))
	if fr != nil {
		ratio := fr.EffectiveCores / float64(fr.ReqCores) * 100
		fmt.Print(TF("core delivery: %d advertised → %.2f actual (%.0f%%) %s\n", fr.ReqCores, fr.EffectiveCores, ratio,
			map[bool]string{true: "✓", false: "⚠"}[ratio >= 85]))
	}
	if dr != nil {
		fmt.Print(TF("disk: write %.0f MB/s | small-file fsync p99 %s %s\n", dr.WriteMBps, fmtMs(dr.Fsync.P99),
			map[bool]string{true: "✓", false: "⚠"}[dr.Fsync.P99 < 10]))
	}
	fmt.Print(TF("steal: avg %.1f%% | peak %.1f%% %s\n", snap.StealAvg, snap.StealMax,
		mark(snap.StealAvg < 3 && snap.StealMax < 25)))
	fmt.Print(TF("sched jitter: p99 %s | peak %s %s\n", fmtMs(snap.JitP99), fmtMs(snap.JitMax),
		mark(snap.JitP99 < 30)))
	if res.Latency.N > 0 {
		fmt.Print(TF("network: RTT p50 %s | p99 %s\n", fmtMs(res.Latency.P50), fmtMs(res.Latency.P99)))
		errRate := float64(res.Errs) / float64(res.Total+res.Errs) * 100
		fmt.Print(TF("concurrency: %.0f req/s @ %d conns | errors %.2f%% %s\n",
			float64(res.Total)/res.Elapsed.Seconds(), res.Conns, errRate, mark(errRate < 1)))
	}
	fmt.Print(TF("downlink: %.1f MB/s (≈%.0f Mbps)%s\n", bw, bw*8, mark(bw > 10)))
	fmt.Println()
	fmt.Println(T("Tips: run `vpsbench steal <url> -w` for 24h to catch peak-hour steal;"))
	fmt.Println(T("      `vpsbench packet <url> -c 100` for 1K/2K/4K packet concurrency;"))
	fmt.Println(T("      `vpsbench conn <url> -c 1000` to probe max concurrent connections."))
}
