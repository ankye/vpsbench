# vpsbench — VPS performance / concurrency / oversell (shared-host steal) tester

Single static binary. Two ways to use, same API:

1. **Gin server + embedded web UI** — run on the VPS under test, open in browser, click buttons.
2. **CLI client** — drive the same API from LAN / headless machines.

English by default (mojibake-safe); add `-lang zh` (or env `VPSBENCH_LANG=zh`) for Chinese.

## Install (one click)

```bash
curl -fsSL https://raw.githubusercontent.com/ankye/vpsbench/main/install.sh | sh
```

The script detects your OS/architecture (linux/darwin × amd64/arm64/386/arm) and installs the
matching static binary from the latest [Release](https://github.com/ankye/vpsbench/releases),
with SHA-256 verification. Pin a version with `VPSBENCH_VERSION=v1.0.0`.

Manual download: grab `vpsbench-<os>-<arch>` from
[Releases](https://github.com/ankye/vpsbench/releases), `chmod +x`, done.

## Quick start

```bash
# On the VPS under test
vpsbench server -addr :8300 -token YOUR_SECRET
ufw allow 8300/tcp

# Option A: browser → http://<vps-ip>:8300  (token box top-right), click to test
# Option B: CLI from anywhere
vpsbench all http://<vps-ip>:8300
vpsbench steal http://<vps-ip>:8300 -w          # keep watching 24h for peak-hour steal
```

## What it measures

| Command / button | Measures |
|---|---|
| `ping` | RTT percentiles + jitter, catches steal-induced latency spikes |
| `load -c 50 [-size N]` | HTTP concurrency: RPS / error rate / p99 (size = response bytes) |
| `packet -c 100` | 1K/2K/4K packet-size ladder: RPS / throughput / percentiles + bottleneck analysis |
| `conn -c 500` | Max concurrent TCP connections (browsers cap ~6 — use CLI) |
| `bw [-up]` | Download / upload bandwidth |
| `cpu` | Single-core speed (M ops/s, comparable across machines) |
| `fill -n 0` | Fill all cores → **core delivery ratio** (oversell / cgroup-quota detection) |
| `disk` | Seq R/W bandwidth + small-file fsync latency (most I/O-steal-sensitive metric) |
| `diag -d 10` | **One-click diagnosis**: which of CPU/GPU/IO/socket/mem/disk loads highest, p50/p95/p99, ideal-cap table, % of ideal |
| `steal [-w]` | **CPU steal% + scheduling jitter report** — the core shared-host steal metrics |
| `all` | Full suite + copy-paste All-in-One server summary |

## Reading the steal metrics

- **steal%** — CPU time the hypervisor admits giving to other tenants (`/proc/stat`).
  Idle-time steal consistently > 2% or frequent red events ⇒ oversold host.
- **scheduling jitter** — actual-vs-expected delay of a 10ms sleep. When vCPUs get parked
  you'll see tens-to-hundreds of ms spikes — that *is* the latency of being stolen.
  Idle-time p99 > 20 ms is abnormal.
- **core delivery** — CPU-seconds actually received ÷ wall time with all cores busy.
  < 85% ⇒ advertised cores inflated or throttled.
- **small-file fsync p99** — > 10 ms ⇒ storage fought over by neighbors.
- steal stuck at 0 but jitter high ⇒ possibly OpenVZ/LXC (steal invisible there) — trust the jitter.

## Build from source

```bash
go build -o vpsbench .
```

Releases are built by **GitHub Actions**: push a `v*` tag (e.g. `git tag v1.0.1 && git push origin v1.0.1`)
and the [release workflow](.github/workflows/release.yml) cross-compiles all platforms
(linux/darwin/windows × amd64/arm64/386/arm), attaches binaries + `SHA256SUMS` to the release automatically.

---

# vpsbench — VPS 性能 / 并发 / 超售(共享宿主机偷取)检测工具

单个静态二进制,两种用法,测同一套 API:

1. **Gin 服务器 + 内嵌网页**:跑在被测 VPS 上,浏览器打开直接点按钮测试。
2. **CLI 客户端**:内网 / 无浏览器环境命令行压测。

默认英语防乱码;任意命令加 `-lang zh`(或环境变量 `VPSBENCH_LANG=zh`)切中文。

## 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/ankye/vpsbench/main/install.sh | sh
```

脚本自动识别 系统/架构(linux/darwin × amd64/arm64/386/arm),从最新
[Release](https://github.com/ankye/vpsbench/releases) 下载对应静态二进制并校验 SHA-256。
指定版本:`VPSBENCH_VERSION=v1.0.0`。

手动下载:到 [Releases](https://github.com/ankye/vpsbench/releases) 取 `vpsbench-<os>-<arch>`,`chmod +x` 即用。

## 快速开始

```bash
# 被测 VPS 上
vpsbench server -addr :8300 -token 你的密码
ufw allow 8300/tcp

# 方式一:浏览器打开 http://<vps-ip>:8300(右上角填令牌),点按钮测试
# 方式二:任意机器命令行
vpsbench all http://<vps-ip>:8300
vpsbench steal http://<vps-ip>:8300 -w          # 挂 24h 抓高峰期偷取
```

## 测试项

| 命令 / 按钮 | 测什么 |
|---|---|
| `ping` | RTT 分位延迟 + 抖动,抓邻居偷取造成的延迟尖刺 |
| `load -c 50 [-size N]` | HTTP 并发压测:RPS / 错误率 / p99(size=响应包字节) |
| `packet -c 100` | **1K/2K/4K 包尺寸并发阶梯**:RPS / 吞吐 / 分位延迟 + 瓶颈分析 |
| `conn -c 500` | 最大并发连接数(浏览器同源 ~6 条限制,测上限用 CLI) |
| `bw [-up]` | 上/下行带宽 |
| `cpu` | 单核算力(M ops/s,跨机器可比) |
| `fill -n 0` | 打满全部核 → **核数兑现率**(超售/cgroup 限速检测) |
| `disk` | 顺序读写带宽 + 小文件 fsync 延迟(I/O 被偷取最敏感) |
| `diag -d 10` | **一键诊断**:CPU/GPU/IO/socket/mem/磁盘 谁负载最高,p50/p95/p99 + 理想值对比表 + 占理想百分比 |
| `steal [-w]` | **CPU steal% + 调度抖动报告**——共享宿主机偷取核心指标 |
| `all` | 全套测试 + 可整段复制的 All-in-One 服务器信息汇总 |

## 怎么读"偷取"指标

- **steal%**:`/proc/stat` 里虚拟化层承认被其他租户占走的 CPU 占比。空闲时持续 >2%
  或频繁红色事件 ⇒ 宿主机超售。
- **调度抖动**:程序 `sleep 10ms` 的实际偏差。vCPU 被挂起时出现几十~几百 ms 尖刺
  ——这就是"被偷取的延迟"的直接体现。空闲时 p99 > 20ms 即异常。
- **核数兑现率**:打满标称核数后实际 CPU 秒 ÷ 墙钟。<85% ⇒ 核数虚标或限速。
- **小文件 fsync p99**:>10ms ⇒ 存储被邻居抢占。
- steal 恒 0 但抖动大 ⇒ 可能是 OpenVZ/LXC(看不到 steal),以抖动为准。

## 源码构建

```bash
go build -o vpsbench .
```

Release 由 **GitHub Actions** 自动构建:推一个 `v*` tag(如 `git tag v1.0.1 && git push origin v1.0.1`),
[release workflow](.github/workflows/release.yml) 会交叉编译全平台
(linux/darwin/windows × amd64/arm64/386/arm),自动把二进制和 `SHA256SUMS` 挂到 Release 上。
