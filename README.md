# vpsbench — VPS 性能 / 并发 / 超售(偷取)检测

单二进制,两种用法,测同一套 API:

1. **Gin 服务器 + 内嵌网页**:跑在被测 VPS 上,浏览器打开直接点按钮测试
2. **CLI 客户端**:内网 / 无浏览器环境命令行压测

## 快速开始

```bash
# 在被测 VPS 上
./vpsbench server -addr :8300            # 可选: -token XXX -dir /tmp
ufw allow 8300/tcp                       # 放行防火墙

# 方式一:浏览器打开 http://<vps-ip>:8300/ 点按钮测试
# 方式二:内网命令行
./vpsbench all http://<vps-ip>:8300
./vpsbench steal http://<vps-ip>:8300 -w     # 挂 24h 观察偷取
```

## 测试项

| 命令 / 按钮 | 测什么 |
|---|---|
| `ping` | RTT 分位延迟 + 抖动,抓邻居偷取造成的延迟尖刺 |
| `load -c 50 [-size N]` | HTTP 并发压测:RPS / 错误率 / p99(size=响应包字节) |
| `packet -c 100` | **1K/2K/4K 包尺寸并发阶梯**:RPS / 吞吐 / 延迟分位 + 瓶颈分析 |
| `diag -d 10` | **一键诊断**:CPU/GPU/IO/socket/mem/磁盘谁负载最高 + p50/p95/p99 + 理想值对比表 |
| `conn -c 500` | 最大并发连接数(TCP 承受力,浏览器有 ~6 连接限制,用 CLI) |
| `bw [-up]` | 上/下行带宽 |
| `cpu` | 单核算力(M ops/s,跨机器可比) |
| `fill -n 0` | 打满全部核 → **核数兑现率**(超售/cgroup 限速检测) |
| `disk` | 顺序读写带宽 + 小文件 fsync 延迟(IO 争抢最敏感) |
| `steal [-w]` | **CPU steal% + 调度抖动报告**(共享宿主机偷取核心指标) |
| `all` | 全套 + 综合结论 |

## 怎么读"偷取"指标

- **steal%**:`/proc/stat` 里虚拟化层承认被其他租户占走的 CPU 时间占比。
  空闲时持续 >2% 或频繁出现红色事件 ⇒ 宿主机超售。
- **调度抖动**:程序 `sleep 10ms` 的实际耗时偏差。vCPU 被挂起时会出现几十~几百 ms
  尖刺 —— 这就是"被偷取的延迟"的直接体现。空闲时 p99 > 20ms 即异常。
- **核数兑现率**:打满标称核数后实际拿到的 CPU 秒 / 墙钟时间。<85% ⇒ 超售或限速。
- **小文件 fsync p99**:>10ms ⇒ 存储被邻居抢占。
- steal 恒为 0 且抖动大:可能是 OpenVZ/LXC,看不到 steal,以抖动为准。

## 构建

```bash
go build -o vpsbench .
# 交叉编译
GOOS=linux  GOARCH=amd64 go build -o vpsbench-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -o vpsbench-darwin-arm64 .
GOOS=windows GOARCH=amd64 go build -o vpsbench.exe .
```
