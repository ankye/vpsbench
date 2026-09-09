# vpsbench — VPS 性能 / 并发 / 超售(共享宿主机偷取)检测工具

[English](README.md) | [简体中文](README.zh-CN.md)

单个静态二进制,两种用法,测同一套 API:

1. **Gin 服务器 + 内嵌网页**:跑在被测 VPS 上,浏览器打开直接点按钮测试。
2. **CLI 客户端**:内网 / 无浏览器环境命令行压测。**免 server**:省略 `<url>`,所有命令进程内起服务直接自测本机。

默认英语防乱码;任意命令加 `-lang zh`(或环境变量 `VPSBENCH_LANG=zh`)切中文。

## 一键验货(新 VPS 到货)

```bash
vpsbench inspect -cores 2 -ram 4 -disk 80 -lang zh   # 8 步验收向导,一路回车
```

7 步检查 + 验收单:配置与订单比对、CPU 真伪与超售、内存、磁盘/IO、
网络(公网 IP 归属/IPv6/DNS/下行带宽/回环吞吐)、**空闲偷取与调度抖动**、
安全基线(sshd、UID 0 用户、爆破记录、防火墙、待升级补丁)。
退出码 0/1/2 = 通过 / 有保留 / 不通过;验收报告存 `~/vpsbench-inspect-*.txt`。

## 一键安装(curl 从 GitHub Releases 下载)

```bash
curl -fsSL https://raw.githubusercontent.com/ankye/vpsbench/main/install.sh | sh
```

脚本自动识别 系统/架构(linux/darwin × amd64/arm64/386/arm),从最新
[Release](https://github.com/ankye/vpsbench/releases) 下载对应静态二进制并校验 SHA-256。
指定版本:`VPSBENCH_VERSION=v1.0.0`。

想直接下载?按平台取对应文件:

```bash
# linux/amd64 为例
curl -fsSL -o vpsbench https://github.com/ankye/vpsbench/releases/latest/download/vpsbench-linux-amd64
chmod +x vpsbench
```

| 平台 | 文件 |
|---|---|
| Linux x86_64 | `vpsbench-linux-amd64` |
| Linux ARM64 | `vpsbench-linux-arm64` |
| Linux 386 / ARMv7 | `vpsbench-linux-386` / `vpsbench-linux-arm` |
| macOS Intel / Apple Silicon | `vpsbench-darwin-amd64` / `vpsbench-darwin-arm64` |
| Windows x64 / ARM64 | `vpsbench-windows-amd64.exe` / `vpsbench-windows-arm64.exe` |

## 快速开始

```bash
# 被测 VPS 上
vpsbench server -addr :8300 -token 你的密码
ufw allow 8300/tcp

# 方式一:浏览器打开 http://<vps-ip>:8300(右上角填令牌,支持 EN/中文 切换),点按钮测试
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
[release workflow](.github/workflows/release.yml) 会交叉编译全平台,自动把二进制和 `SHA256SUMS` 挂到 Release 上。
