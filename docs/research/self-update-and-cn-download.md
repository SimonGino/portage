# Research：自替换、Exec 自重启、浏览器侧检测与国内下载路径（#152）

调研日期 2026-09-29。只记事实与建议，不裁决；裁决在地图票 #150。所有结论回溯一手来源，能本机复现的都跑了一遍（macOS 27.0 arm64 / Alpine Linux aarch64 容器），查不到的点明说查不到。

## 事实源

抓取 / 核对日期均为 2026-09-29。

1. Go 1.26.4 标准库源码（本机 `/opt/homebrew/Cellar/go/1.26.4/libexec/src`）：`syscall/exec_unix.go`（`Exec`、`ForkLock` 注释、`CloseOnExec`）、`net/sock_cloexec.go`、`net/sockopt_linux.go` / `net/sockopt_bsd.go`（`setDefaultListenerSockopts`）、`os/file_unix.go`（`O_CLOEXEC`）、`os/executable_procfs.go`、`os/executable_darwin.go`、`runtime/proc.go`（`syscall_runtime_BeforeExec`）、`cmd/link/internal/ld/macho.go`（`machoCodeSign`）、`cmd/internal/codesign/codesign.go`；`go doc` 输出：`os.Executable`、`os.CreateTemp`、`net/http.Server.Shutdown`、`os/signal.NotifyContext`
2. Linux man-pages（man7.org）：[rename(2)](https://man7.org/linux/man-pages/man2/rename.2.html)、[open(2)](https://man7.org/linux/man-pages/man2/open.2.html)、[execve(2)](https://man7.org/linux/man-pages/man2/execve.2.html)、[unlink(2)](https://man7.org/linux/man-pages/man2/unlink.2.html)、[systemd.service(5)](https://man7.org/linux/man-pages/man5/systemd.service.5.html)
3. Go issue [#22315 os: StartProcess ETXTBSY race on Unix systems](https://github.com/golang/go/issues/22315)（open）；Go issue [#42684 cmd/go: macOS on arm64 requires codesigning](https://github.com/golang/go/issues/42684)（closed 2020-12-01，里程碑 Go1.16，CL 272254/272256 `cmd/internal/codesign: new package` / `cmd/link: code-sign on darwin/arm64`）
4. Apple — [Updating Mac Software](https://developer.apple.com/documentation/security/updating-mac-software)（Security 框架文档）；[LSFileQuarantineEnabled](https://developer.apple.com/documentation/bundleresources/information-property-list/lsfilequarantineenabled)；Apple Developer Forums [thread 758098 "Golang binary self-update - killed 9"](https://developer.apple.com/forums/thread/758098)（DTS Quinn 答复）
5. 库：[minio/selfupdate](https://github.com/minio/selfupdate)（README、`apply.go`、`go.mod`、GitHub API 元数据）、[creativeprojects/go-selfupdate](https://github.com/creativeprojects/go-selfupdate)（README、`update.go`、`go.mod`）、[inconshreveable/go-update](https://github.com/inconshreveable/go-update)（README、LICENSE）
6. GitHub Docs — [Releases REST：Get the latest release](https://docs.github.com/en/rest/releases/releases?apiVersion=2022-11-28#get-the-latest-release)；[Rate limits for the REST API](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28)；[Using CORS and JSONP](https://docs.github.com/en/rest/using-the-rest-api/using-cors-and-jsonp-to-make-cross-origin-requests?apiVersion=2022-11-28)；[Best practices（conditional requests）](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api?apiVersion=2022-11-28)；[Authenticating to the REST API](https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api?apiVersion=2022-11-28)；[Setting repository visibility](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/setting-repository-visibility)；[Linking to releases](https://docs.github.com/en/repositories/releasing-projects-on-github/linking-to-releases)；以及对 `api.github.com/repos/goreleaser/goreleaser/releases/latest` 的实测（curl，含 CORS 预检）
7. jsDelivr — [jsdelivr/jsdelivr README（GitHub 与 Caching 节）](https://github.com/jsdelivr/jsdelivr)；[@jsDelivr 2021-12-20 推文（ICP 丢失）](https://x.com/jsDelivr/status/1472870623051456522)；issue [#18407 Can jsdelivr regain its ICP filing in mainland China?](https://github.com/jsdelivr/jsdelivr/issues/18407)；[jsdmirror/JSDMirror README](https://github.com/jsdmirror/JSDMirror)
8. GitHub 代理：[hunshcn/gh-proxy README](https://github.com/hunshcn/gh-proxy)；[ghproxy.link（ghfast.top 的地址发布页）](https://ghproxy.link/)；[ghproxy.net 首页](https://ghproxy.net/)；[gh-proxy.com 首页](https://gh-proxy.com/)；第三方测速文 [冲浪笔记 2026-05](https://www.chonglangbiji.com/security/github-release-download-mirror-speed-comparison-may-2026/)；本机 curl 逐个探活
9. 国内可达性第三方观测：[GreatFire Analyzer — api.github.com](https://en.greatfire.org/api.github.com)；[chinafirewalltest.com — api.github.com](https://www.chinafirewalltest.com/?siteurl=api.github.com)
10. 本仓库 `cmd/portage/main.go`（`signal.NotifyContext` / `srv.Shutdown` 那段）

> 本机网络出口经代理落在美国（`ipinfo.io/country` = US），所以本文的 curl 探测只能证明「站点活着」，**不能**证明「国内可达」；国内可达性只引第三方观测。

---

## 一、原子自替换

### 1. Linux：ETXTBSY 与 rename 的正确姿势

| 事实 | 依据 |
| --- | --- |
| 以写模式打开正在执行的文件报 `ETXTBSY`："path refers to an executable image which is currently being executed and write access was requested." | open(2)（源 2）；本机容器实测 `open /opt/selfexec: text file busy` |
| `execve` 一个**正被某进程以写模式打开**的文件也报 `ETXTBSY`："The specified executable was open for writing by one or more processes." → 临时文件写完必须 `Close()` 再 `Rename`/`Exec` | execve(2)（源 2） |
| `rename` 覆盖已存在的 newpath 是原子的："If newpath already exists, it will be atomically replaced, so that there is no point at which another process attempting to access newpath will find it missing." man page 没有任何「newpath 正在执行则拒绝」的条款——实测 rename 覆盖运行中的二进制成功 | rename(2)（源 2）；容器实测 `rename: <nil>` |
| 旧 inode 在被 rename 顶掉后仍活着，直到最后一个引用关闭（运行中的进程 mmap 着它）："If the name was the last link to a file but any processes still have the file open, the file will remain in existence until the last file descriptor referring to it is closed." → 旧进程继续跑旧代码，不会崩 | unlink(2)（源 2） |
| 跨文件系统 `rename` 报 `EXDEV`："oldpath and newpath are not on the same mounted filesystem." → 临时文件**必须建在可执行文件同目录**（`filepath.Dir(exe)`），不能用 `os.TempDir()`（`/tmp` 常是 tmpfs） | rename(2)（源 2） |
| 目录不可写的错误形态：`EACCES`——"Write permission is denied for the directory containing oldpath or newpath"。实际会**更早**在建临时文件那步就失败，错误串形如 `open /usr/local/bin/.portage-1424282076.new: permission denied`；目录带 sticky bit 且文件属主不是自己时 rename 报 `EPERM` | rename(2)、open(2)（源 2）；容器实测（`chmod 555` 目录 + 非 root）`panic: open /ro/.portage-*.new: permission denied` |
| 权限位：`os.CreateTemp` 建的文件是 `0o600 (before umask)`，rename 后**不会**继承旧文件的 0755 → 必须显式 `Chmod(0o755)`（或 `os.Stat(旧文件).Mode()` 抄过来）；属主是当前进程 uid，不是旧文件属主 | `go doc os.CreateTemp`（源 1） |
| `os.Executable()` 在 Linux 读 `/proc/self/exe`，旧文件被顶掉后 readlink 结果带 ` (deleted)` 后缀，**Go 已经把这个后缀剥掉了**（`executable_procfs.go`：`stringslite.TrimSuffix(path, " (deleted)")`），所以替换后再调 `os.Executable()` 仍得到原路径，Exec 它就是新文件。稳妥起见仍建议**替换前**先取一次路径存起来 | 源 1；容器实测 `os.Executable after rename: /opt/selfexec` |
| 若启动路径是符号链接：Linux 的 `/proc/self/exe` 已解析到真实文件；macOS 的 `os.Executable` 来自 runtime 记的 `executablePath`（可能仍是链接路径）。文档原话："If a symlink was used to start the process, depending on the operating system, the result might be the symlink or the path it pointed to. If a stable result is needed, path/filepath.EvalSymlinks might help." → 替换目标先过 `filepath.EvalSymlinks`，别把符号链接本身覆盖成普通文件 | `go doc os.Executable`（源 1） |
| Go #22315 的 ETXTBSY 竞态（写完的可执行文件 fd 被另一线程并发 fork 的子进程带走、来不及 exec 释放）只在**本进程还会并发 fork/exec 别的子进程**时发生。Portage 不 spawn 子进程，`Exec` 又是替换自身而非 fork，此竞态不适用；但「写完先 Close 再 Exec」仍是硬要求（见上 execve 条） | 源 3（issue 至今 open，Ian Lance Taylor："all files opened using the Go standard library have O_CLOEXEC set"） |

### 2. macOS：签名缓存与隔离属性

| 事实 | 依据 |
| --- | --- |
| macOS **没有** ETXTBSY 这一层保护：本机实测以写模式打开正在运行的 Go 二进制成功（`open running exe for write: <nil>`） | 本机实测（macOS 27.0 arm64） |
| 但**原地改写**一个已签名 Mach-O 会踩内核签名缓存："macOS caches information about the code's signature in the kernel. It doesn't flush that cache when you modify the file's contents. Modifying the file in place yields a mismatch between the file's contents and the in-kernel cache, which can cause a hard-to-reproduce code-signing crash the next time you run the tool."——症状是随机崩、崩溃报告含 `Code Signature Invalid`、重启机器后消失。Apple 给的正解就是**写新文件 + `rename`**（"The new file gets its own in-kernel cache, built from the contents of that new file."），示例代码就是 `open(temporaryPath, .create, 0755)` → `writeAll` → `rename(temporaryPath, executablePath)` | Apple「Updating Mac Software」（源 4）；DTS Quinn 在 thread 758098 把 Go 自更新的 `Killed: 9` 首先归到这条 |
| 所以 Linux 与 macOS 的姿势**同一套**：临时文件 + rename。rename 顶掉的旧 vnode 由运行中的进程持有，不影响老进程；实测 rename 后 Exec 成功、新进程 `codesign -dv` 仍是 `Signature=adhoc` | 本机实测 |
| Apple Silicon 要求可执行文件至少有 ad-hoc 签名，否则启动即 SIGKILL（issue 标题即 "macOS on arm64 requires codesigning"）。Go 1.16 起 **Go 自己的链接器**在链接 darwin/arm64 时就打上 ad-hoc 签名（`cmd/link/internal/ld/macho.go`：`// machoCodeSign code-signs Mach-O file fname with an ad-hoc signature.`；`cmd/internal/codesign` 包注释："It uses the same ad-hoc signing algorithm as the Darwin linker."）。这套是纯 Go 实现，不调 Apple 的 `codesign`，所以 **Linux 上交叉编译（GoReleaser 场景）出来的 darwin/arm64 产物同样带签名**。本机 `go build` 产物 `codesign -dv`：`flags=0x20002(adhoc,linker-signed)` | 源 1、源 3（#42684 里程碑 Go1.16，CL 272256）；本机实测 |
| 隔离属性（quarantine）由**应用主动打**：`LSFileQuarantineEnabled` 是 Info.plist 键，"A Boolean value indicating whether the files this app creates are quarantined by default."——curl、Go 写的 `portage upgrade` 都不是带 Info.plist 的 app，不会打 `com.apple.quarantine`。本机实测 curl 落盘文件的 xattr 只有 `com.apple.provenance`，没有 quarantine；Go 编出的二进制亦然。→ `curl | sh` 与 `portage upgrade` 两条路都**不过 Gatekeeper**；只有浏览器下载的产物才会被隔离、需要 `xattr -d com.apple.quarantine` | 源 4；本机实测 `xattr -l` |
| `com.apple.provenance` 是系统自动记的来源属性，不阻止执行；本次实测带着它 Exec 成功 | 本机实测 |

### 3. 现成库 vs 手写

| | inconshreveable/go-update | minio/selfupdate | creativeprojects/go-selfupdate |
| --- | --- | --- | --- |
| 许可证 | Apache-2.0（LICENSE 文件；GitHub API 识别为 NOASSERTION，因 LICENSE 是节选形式） | Apache-2.0（"Original work at github.com/inconshreveable/go-update, modified for the needs within MinIO project"） | MIT |
| 活跃度 | 最后提交 2016-01-12，无 tag；README 明写 "The master branch of go-update is not guaranteed to have a stable API over time"，建议 vendor | 最新 tag v0.6.0（2023-01-22），最后推送 2025-10-21；`go 1.24.0` | 最新 tag v1.6.0，最后推送 2026-08-05；`go 1.25.12` |
| 直接依赖 | 无 | `aead.dev/minisign`（间接 x/crypto、x/sys） | `google/go-github/v86`、`code.gitea.io/sdk/gitea`、`gitlab.com/gitlab-org/api/client-go`、`Masterminds/semver/v3`、`ulikunitz/xz`、`x/crypto`、`yaml.v3` |
| 做了什么 | `Apply(io.Reader, Options)`：同目录写 `.target.new` → 旧文件 rename 成 `.target.old` → 新文件 rename 到位 → 删 `.old`（Windows 删不掉就隐藏）；最后一步失败则把 `.old` 挪回去（`RollbackError`）；可选 checksum、ECDSA 签名、bsdiff patch、`TargetMode`、`OldSavePath` | 同上流程（`apply.go` 第 33、91~101 行注释逐条列出），签名验证换成 minisign；多了 `.target.check-perm` 探针文件试目录可写、`.old` 路径归一 | 在 `update.Apply`（自带的 go-update fork，`update/` 子包）之上加：查 GitHub/Gitea/GitLab 最新 release、按 `{cmd}_{goos}_{goarch}{.ext}` 匹配资产、解 zip/tar.gz/tar.xz/bzip2、校验（每资产 `.sha256` 或单个 checksums 文件、ECDSA `.sig`）、`UpdateSelf` / `UpdateTo` / `DetectLatest` |
| 不做什么 | 下载、版本比较、解压、重启 | 同左 | 重启、优雅收场 |

事实性观察（供裁决，不裁决）：

- #150 已定的链路（下载指定版本 → `checksums.txt` sha256 → 临时文件 → rename → drain → Exec）里，三个库覆盖的只有中间「临时文件 → rename」那一段，且都不含 Exec/收场。这段核心是 `os.CreateTemp` + `io.Copy` + `Chmod` + `Sync` + `Close` + `os.Rename`，约 30~60 行（含错误处理）；本文实验程序 50 行就跑通了两平台。
- go-update 十年没动、无 tag；go-selfupdate 为了 Gitea/GitLab 拉进三套 SDK；minio/selfupdate 最小但也带 minisign（#150 已否签名）。如果要引库，minio/selfupdate 是三者中依赖最轻的；它比手写多出的只有 `.old` 回滚（#150 已否降级/回滚）与 Windows 分支（#150 不出 Windows）。
- 三个库的 `.old` 流程都是「两次 rename」而非「一次 rename 覆盖」，中间存在目标路径短暂不存在的窗口；一次 rename 覆盖（Apple 示例与 rename(2) 原子性条款所指）没有这个窗口。

### 4. 建议的最小序列（临时文件 + rename + drain + Exec）

```
exe, _ := os.Executable()                 // 替换前取；再过 filepath.EvalSymlinks
tmp, err := os.CreateTemp(filepath.Dir(exe), ".portage-*.new")   // 同目录（EXDEV）；目录不可写在此处即报 EACCES
    io.Copy(tmp, tar 里的 portage 条目)   // 边 copy 边喂 sha256.Hash，读完与 checksums.txt 比对；不匹配 → Remove(tmp) 返回，别 rename
    tmp.Chmod(0o755)                      // CreateTemp 是 0600
    tmp.Sync(); tmp.Close()               // 不 Close 就 exec 会 ETXTBSY；Sync 防断电后半截文件
os.Rename(tmp.Name(), exe)                // 原子覆盖；Linux/macOS 同一套；老进程继续持旧 inode
—— 到这里文件替换完成，进程还是老代码 ——
srv.Shutdown(30s ctx)                     // 先关 listener 再等在途连接（Shutdown 文档）
db.Close()                                // Exec 不跑 defer，必须显式关（见下节）
syscall.Exec(exe, os.Args, os.Environ())  // 同 PID 变成新程序；只在失败时返回
```

具体坑：

1. **校验发生在 rename 之前**——`checksums.txt` 校验的是 tar.gz 整包（GoReleaser 默认），所以 sha256 要喂压缩包字节流而不是解出来的二进制；下载到内存/临时文件 → 校验 → 再解包写 `.new`。
2. **tar 解包只取那一个可执行条目**，权限自己设 0755，不信 tar 头（GoReleaser 打包的 mode 通常没问题，但别依赖）。
3. **`CreateTemp` 失败 = 目录不可写**，这正是 #150「分发形态识别 = 可执行文件目录可写性」那一判的落点，同一个错误路径。
4. **rename 之后到 Exec 之前进程若崩/被杀**，磁盘上已是新版本、内存跑的是旧版本，systemd `Restart=always` 拉起来就是新的——这是安全侧；反向（Exec 失败）见下节第 6 条。
5. macOS 不要图省事「打开原文件 truncate 重写」——没有 ETXTBSY 拦着，但会撞 Apple 说的签名缓存 `Code Signature Invalid`。

---

## 二、`syscall.Exec` 自重启

| 事实 | 依据 |
| --- | --- |
| `Exec` 就是 `execve(2)`："There is no new process; many attributes of the calling process remain unchanged (in particular, its PID)." | `go doc syscall.Exec`（源 1）；execve(2)（源 2）；两平台实测 `child: pid` 与 parent 相同 |
| fd 跨 exec 的规则："By default, file descriptors remain open across an execve(). File descriptors that are marked close-on-exec are closed." Go 标准库开的 fd 全部带 CLOEXEC：`os.OpenFile` 走 `open(name, flag\|syscall.O_CLOEXEC, ...)`，`net` 建 socket 走 `socketFunc(family, sotype\|SOCK_NONBLOCK\|SOCK_CLOEXEC, proto)`，`syscall` 的 `ForkLock` 注释列了 Socket/Open/Dup 三类都用 CLOEXEC 变体 | 源 1（`os/file_unix.go:261`、`net/sock_cloexec.go:20`、`syscall/exec_unix.go:55~65`） |
| 监听 socket 无论关没关都不会漏进新进程：实测故意**不关 listener** 直接 Exec，新进程再 `Listen` 同端口成功（`child: re-listen err = <nil>`，Linux 与 macOS 均是）。正常路径下 `srv.Shutdown` 本来就先关 listener："Shutdown works by first closing all open listeners, then closing all idle connections, and then waiting indefinitely for connections to return to idle" | 源 1；两平台实测 |
| `EADDRINUSE` / TIME_WAIT：Go 的 `Listen` 对每个 listener 默认设 `SO_REUSEADDR`（`setDefaultListenerSockopts`，Linux 与 BSD/darwin 两份实现都是 `SetsockoptInt(s, SOL_SOCKET, SO_REUSEADDR, 1)`），刚关闭的连接处于 TIME_WAIT 不影响新进程重新绑定。**不需要**自己设 `SO_REUSEADDR`，也不需要 `SO_REUSEPORT` | 源 1（`net/sockopt_linux.go:28`、`net/sockopt_bsd.go:43`） |
| **`Exec` 不跑 defer**——`main.go` 里 `defer db.Close()` / `defer stop()` 都不会执行；execve 后所有其他线程被销毁（"All threads other than the calling thread are destroyed during an execve()"），后台 goroutine（用量 prune 的 ticker）直接消失。SQLite 的 fd 带 CLOEXEC 会被内核关掉、不会泄漏，但**没有干净 close 就没有 WAL checkpoint**，下次打开要恢复 → 显式 `db.Close()` 再 Exec | 源 1、源 2 |
| 信号处置在 exec 后重置为默认（"The dispositions of any signals that are being caught are reset to the default"）——`signal.NotifyContext` 装的 handler 随旧镜像消失，新进程 main 里重新装即可，无需先 `stop()` | execve(2)（源 2） |
| Go 的 `Exec` 在调 execve 前：`runtime_BeforeExec()` 锁住线程创建、darwin 上等待挂起的抢占信号（issue #41702），再把 `RLIMIT_NOFILE` 恢复成进程启动时的原值（Go 运行时启动时会把软限提到硬限，exec 前还原）。这两件事运行时都替你做了，不用管 | 源 1（`syscall/exec_unix.go:284~289`、`runtime/proc.go:5252~5263`） |
| **Exec 失败只有一种可见形态：返回 error**（新文件不可执行 `EACCES`、格式坏 `ENOEXEC`、被人删了 `ENOENT`）。此时进程仍是旧代码，但 listener 已经被 Shutdown 关掉、DB 已 close——不能「当没事继续服务」。可选处置：记日志后 `os.Exit(1)` 交给 systemd `Restart=always` 拉起磁盘上的（新）文件；或者不退出、重新 `Listen` 兜底。sha256 校验能挡住「下载坏了」这一类，挡不住「新版本自身有 bug 起不来」——那种情况 Exec 成功后新进程崩、systemd 反复拉起，磁盘上已无旧版本（#150 已否回滚，只能 `install.sh` 钉旧版） | 源 2；推理 |
| PID 1 / 容器：#152 明说可略；docker 形态面板不出按钮（#150），不展开 | — |

### systemd 下 Exec 后 unit 状态

- Type=simple/exec/notify 的 main PID 判定："Unless Type=forking is set, the process started via this command line will be considered the main process of the daemon."——systemd 记的是 **PID**，exec 不换 PID，unit 仍 `active (running)`、`MainPID` 不变、不触发 `Restart=`、日志流（stdout/stderr fd 无 CLOEXEC，是 systemd 传进来的）继续接到 journal。与 `systemctl restart` 的区别只是 `ActiveEnterTimestamp` 不变、`ExecMainStartTimestamp` 不变。（源 2 systemd.service(5)）
- `Restart=always`："the service will be restarted regardless of whether it exited cleanly or not."——覆盖上表「Exec 失败后 `os.Exit(1)`」与「新版本启动即崩」两种情形。（源 2）
- `ExitType=cgroup` 与本案无关（我们没有子进程）；`KillMode`、`TimeoutStopSec` 与 `srv.Shutdown` 的 30s 是两把不同的表——如果日后 `systemctl stop` 也要等满 30s drain，`TimeoutStopSec` 得 ≥ 30s，否则 systemd 先 SIGKILL。这一点是样例文件（`deploy/portage.service`，尚不存在）要写的。

### 在 `cmd/portage/main.go` 里怎么接

现状（`main.go:74`、`:171~189`）：`ctx, stop := signal.NotifyContext(...)`；`srv.ListenAndServe()` 跑在 goroutine 里向 `errCh` 报错；主 goroutine `select { case err := <-errCh: return err; case <-ctx.Done(): Shutdown(30s) }`。`db` 由 `defer db.Close()` 在 `run()` 返回时关。

两种接法（事实描述，选哪种交 PO / 实现票）：

- **A. 在 `run()` 内 Exec**：`select` 加第三支 `case exe := <-upgraded:`（升级 handler 替换完文件后往 channel 送路径），同样调 `srv.Shutdown(30s)`，然后**手动** `db.Close()`，再 `syscall.Exec(exe, os.Args, os.Environ())`；Exec 返回即失败，`return err`。缺点：绕开了 `run()` 的 defer 链，多一个 db 生命周期的口子。
- **B. 用哨兵错误出 `run()`**：第三支只做 Shutdown 然后 `return errRestart`（或带路径的自定义 error）；`run()` 的 defer 正常跑（`db.Close()`、`stop()`），`main()` 收到 `errors.Is(err, errRestart)` 时 Exec。所有资源沿现有 defer 收场，Exec 是 `main()` 最后一行；Exec 失败 `main()` 按现有路径打日志 `os.Exit(1)`。**更贴合现状**，diff 最小。
- 两种接法里，升级 handler 与 `portage upgrade` 子命令**共用同一段下载→校验→替换函数**（#150 已定），差别只在替换成功后：子命令直接 Exec（或只打印「已替换，请重启」——纯转发机上子命令是从 shell 跑的，Exec 只会把 shell 里那个一次性进程变成网关，这不是想要的；子命令路径应改为给运行中的实例发信号或直接提示 `systemctl restart`），面板路径走上面的 channel。这个分岔是实现票要写清的点，本文只记事实：**`syscall.Exec` 只对「当前正在服务的那个进程」有意义**。
- `os.Args` 原样传：`-config`、`-listen` 一类 flag 才能带到新进程；`argv[0]` 保持 `os.Args[0]`。
- Exec 之前 `log.Info` 一行「文件已替换为 vX.Y.Z，正在自重启」，因为 Exec 成功后旧镜像没有机会再写日志。

---

## 三、浏览器侧检测：`GET /repos/{owner}/{repo}/releases/latest`

| 事实 | 依据 |
| --- | --- |
| CORS：所有来源可用，响应带 `access-control-allow-origin: *`；预检 `OPTIONS` 回 204，`access-control-allow-methods: GET, POST, PATCH, PUT, DELETE`，`access-control-max-age: 86400`，`access-control-allow-headers` 含 `Accept-Encoding, X-GitHub-Api-Version, If-None-Match, User-Agent...`；`access-control-expose-headers` 含 `ETag, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Used, Retry-After` 等 → 前端能读到限额与 ETag | 源 6（Using CORS）；本机实测 `curl -H Origin` 与 `OPTIONS` 预检 |
| 一个不带自定义头的 `fetch(url)` 是「简单请求」，不触发预检；加 `Accept: application/vnd.github+json` 仍属简单请求（Accept 是 CORS 安全列表头）；加 `X-GitHub-Api-Version` 会触发预检（允许，多一次 RTT） | [Fetch 标准 CORS-safelisted request-header](https://fetch.spec.whatwg.org/#cors-safelisted-request-header) + 上行实测的 allow-headers |
| 匿名限额："The primary rate limit for unauthenticated requests is 60 requests per hour"，按**来源 IP** 计（"associated with the originating IP address, not with the user or application"）；超限回 `403` 或 `429`，`x-ratelimit-remaining: 0`。实测响应头：`x-ratelimit-limit: 60`、`x-ratelimit-resource: core`、`cache-control: public, max-age=60, s-maxage=60` | 源 6（Rate limits）；本机实测 |
| 条件请求（`If-None-Match` + ETag）回 304 时不计入限额的豁免**只对已认证请求**："does not count against your primary rate limit if a 304 response is returned and the request was made while correctly authorized." → 匿名浏览器侧 304 也算一次；靠 localStorage 缓存一天（#150 已定）而不是靠 ETag 省额度 | 源 6（Best practices） |
| "latest" 的定义："The latest release is the most recent non-prerelease, non-draft release, sorted by the created_at attribute." 并且 "The created_at attribute is the date of the commit used for the release, and not the date when the release was drafted or published." → 排序键是**被打 tag 的那个 commit 的日期**；给旧 commit 补发 release 不会成为 latest | 源 6（Releases） |
| 返回体形状（实测 goreleaser/goreleaser v2.18.2）：顶层 `tag_name`（"v2.18.2"）、`name`、`draft`、`prerelease`、`html_url`（`https://github.com/<o>/<r>/releases/tag/<tag>`）、`published_at`、`body`（release 说明）；`assets[]` 每项 `name`、`browser_download_url`（`https://github.com/<o>/<r>/releases/download/<tag>/<name>`）、`size`、`content_type`、`state`（"uploaded"）、`digest`（`"sha256:<hex>"`，文档标为 string or null） | 源 6；本机实测 |
| `assets[].digest` 是 GitHub 自己算的资产 sha256——与 GoReleaser `checksums.txt` 里的值同源可互验；但 #150 已定信任根是 checksums.txt，这里只记有此字段 | 本机实测 |
| 无 release 时回 `404`（文档状态码只列 200/404）。**SimonGino/portage 今日状态：public、0 个 release，此接口实测 404** → 前端在没发过 Release 的窗口期一定拿到 404，要当「无更新」静默处理，且**别把 404 缓存成一天**（首个 Release 发出后 60s 内接口就会变 200） | 源 6；`gh api repos/SimonGino/portage`、本机实测 |
| private 仓库对匿名请求同样是 **404**（不是 403）："If you try to use a REST API endpoint without a token or with a token that has insufficient permissions, you will receive a 404 Not Found or 403 Forbidden response." → 前端分不清「私有」与「没 release」，两者都走静默；private→public 切换后该接口立刻按上面的 60s 缓存生效，release 与资产不需要重建（切换文档只提代码/Actions 日志可见、star/watcher 清零、push ruleset 关闭，未提 release 有任何特殊处理） | 源 6（Authenticating、Setting repository visibility） |
| 资产下载链：`github.com/<o>/<r>/releases/download/<tag>/<asset>` → `302` → `release-assets.githubusercontent.com/...`（带签名、有过期时间的 URL）；`github.com/<o>/<r>/releases/latest/download/<asset>` → `302` → 上面的 tag 形式。后者让 `install.sh` **不经 api.github.com** 就能取「最新」资产——前提是资产名里不含版本号（GoReleaser `name_template` 可配），或先 `curl -sI .../releases/latest` 从 `Location` 头取 tag | 源 6（Linking to releases）；本机实测 |
| `api.github.com` 国内浏览器侧可达性：GreatFire 2026-09-26 的观测记为「未封锁」（近 90 天 1 次有效测试、0% 中断，并注明此前曾标为 partly disrupted、后按新方法学改判）；chinafirewalltest.com 五个测点（北京/深圳/内蒙古/黑龙江/云南）全部 BLOCKED（页面未标日期）；中文社区 2025~2026 的记述一致是「DNS 污染、时通时断」。**结论：不可靠、不可假定**——前端检测必须是带超时的尽力而为，失败不打扰面板 | 源 9；社区文（二手，仅作旁证） |

---

## 四、国内下载路径

### 1. jsDelivr 镜 GitHub raw

| 事实 | 依据 |
| --- | --- |
| URL 形状 `https://cdn.jsdelivr.net/gh/<user>/<repo>@<version>/<file>`，`<version>` 可以是 tag、commit hash、分支名、semver 范围，或省略 / `@latest`（无 tag 时回退默认分支） | 源 7（README GitHub 节） |
| 缓存时效（README Caching 节）：固定版本 / commit hash "effectively forever"（1 年缓存头 + 永久 S3）；版本别名与 `@latest` **7 天**；**分支名 12 小时**。本机实测 `@main` 响应头 `cache-control: public, max-age=604800, s-maxage=43200` | 源 7；本机实测 |
| → `install.sh` 走 `@main` 改了要等最多 12 小时才刷新；走 `@<tag>` 永久缓存（改了脚本必须换 tag）；purge API 只对 semver 版本生效 | 源 7 |
| 限制：GitHub 源单文件 > 20 MB 不服务，仓库/包 > 150 MB 不服务（README 原文 "Packages larger than 150 MB or single files larger than 20 MB (in the case of GitHub) are not supported by default."）→ 只适合镜 `install.sh` 这种小文件，**不能**镜 Release 资产（Release 资产也不在 `gh/` 路径的服务范围内——它镜的是仓库树） | 源 7 |
| 国内可用性：2021-12-20 jsDelivr 官方推文 "Unfortunately today jsDelivr unexpectedly lost its ICP license in China. As effect the regional CDN disabled our account."；issue #18407 里维护者表示不会把域名迁到中国注册商、无资源办 ICP；社区（含 JSDMirror 项目动机）的描述是此后 `cdn.jsdelivr.net` 在国内 DNS 污染 / TCP 重置、时好时坏。**结论：不可当作国内稳定入口**，只能作为「有时能通」的候选之一 | 源 7 |
| 国内替代镜像：`cdn.jsdmirror.com` / `cdn.jsdmirror.cn`（腾讯 EdgeOne 赞助、个人运营、无可用性承诺），同步 jsDelivr 的 `gh/` 路径，URL 只需替换域名。本机实测 `cdn.jsdmirror.com/gh/goreleaser/goreleaser@main/LICENSE.md` 200 | 源 7（JSDMirror README）；本机实测 |

### 2. 公共 GitHub Release 代理（探活日期 2026-09-29）

统一 URL 拼法 = **代理域名 + `/` + 完整原始 URL**（hunshcn/gh-proxy README："直接在 copy 出来的 url 前加 `https://<代理>/` 即可"），例如

```
https://ghfast.top/https://github.com/SimonGino/portage/releases/download/v0.5.0/portage_0.5.0_linux_amd64.tar.gz
https://ghfast.top/https://raw.githubusercontent.com/SimonGino/portage/main/install.sh
```

支持 release 资产、archive、`raw.githubusercontent.com`、`blob` 路径、gist；代理在服务端跟随 GitHub 的 302 到 `release-assets.githubusercontent.com`，客户端只需能连代理域名。（源 8）

本机 curl 拉 `goreleaser/goreleaser v2.18.2/checksums.txt`（5271 B）并校对内容，结果：

| 域名 | 状态 | 备注 |
| --- | --- | --- |
| **ghfast.top** | 200，0.7 s | 原 `ghproxy.com` 团队的现用域名，地址发布页 `ghproxy.link`；raw 路径亦 200 |
| **gh-proxy.com** | 200，0.6 s | hunshcn/gh-proxy 作者运营（首页自称 GH-Proxy 2.0，列 Git Clone / Release / Raw / Zip / API） |
| **ghproxy.net** | 200，1.1 s | 首页说明 release/archive 走 CF 加速，文件类会 302 到 jsDelivr（→ 对 `install.sh` 这类 raw 文件等于又回到 jsDelivr，国内不一定通） |
| gh-proxy.org、ghproxy.vip、gh.llkk.cc、gh.ddlc.top、gh.zwy.one、ghfile.geekertao.top、ghproxy.cxkpro.top | 200 | 第三方测速文里的候选，今日活着 |
| mirror.ghproxy.com、ghproxy.cc、github.moeyy.xyz、hub.gitmirror.com、raw.ihtw.moe、gh.xxooo.cf、ghp.ci | 连接失败 / 000 | 2024~2025 文章里常见的域名，今日已死；`ghp.ci` 在 `ghproxy.link` 页面上仍被列出但 TLS 握手失败 |

要点：

- **这类站点更替以月计**（第三方测速文的大意：镜像站可用性变化很快，今天最快的明天可能就下线，用前先 `curl -I`）。上表 7 个死域名全是一两年前的「推荐」。`install.sh` / `portage upgrade` 里写死任何一个都会过期，`PORTAGE_DOWNLOAD_BASE` 让用户自己换是对的；内置默认值只能选一个并接受它会死。
- 各家限额、单文件大小上限没有一致的公开说明（ghfast.top 首页是纯前端渲染，抓不到文案；gh-proxy.com、ghproxy.net 首页无限额声明；hunshcn/gh-proxy 自建版 README 提到 Python 版可配文件大小上限、Cloudflare Workers 免费版 10 万次/天）。Portage 的 tar.gz 在 10~20 MB 量级，实测 checksums.txt 没问题，**大文件是否被限没有实测**（本机出口不在国内，测了也不算数）。
- 代理与 checksums.txt 同源同经代理——#150 已把「校验文件同经代理」写成已知边界，本文不再展开。
- **`PORTAGE_DOWNLOAD_BASE` 的语义要定成「前缀」而不是「替换 `https://github.com`」**：所有 ghproxy 类都是前缀模型（域名后直接跟完整 URL），而 jsDelivr/JSDMirror 是另一种路径形状（`gh/<o>/<r>@<ref>/<file>`）且不服务 Release 资产，两者不可能用同一个变量兼容。前缀模型下「不设变量 = 空前缀 = 直连 GitHub」自然成立。
- 「探到连不上 github.com 就换代理」（#150）在脚本里可以是 `curl -sI --max-time 5 https://github.com` 失败即回退；`portage upgrade` 里对应 `http.Client{Timeout}` 首次 HEAD 失败即回退。两条路都要把「代理也失败」明确报出来，别静默换第三家。

---

## 五、给 #150 的一句话摘要

- 自替换：Linux 有 ETXTBSY、macOS 有签名缓存，两者的正解相同——同目录临时文件 + `Chmod(0755)` + `Sync/Close` + 一次 `os.Rename` 覆盖；目录不可写在建临时文件那步就报 `permission denied`。Go 交叉编译的 darwin/arm64 自带 ad-hoc 签名，curl / Go 下载不打 quarantine，macOS 路径无额外坑。
- Exec：同 PID；Go 全部 fd 带 CLOEXEC、listener 默认 `SO_REUSEADDR`，新进程重新 `Listen` 不撞 `EADDRINUSE`（两平台实测）；**Exec 不跑 defer，`db.Close()` 要显式**；systemd 下 unit 仍算同一实例，`Restart=always` 只兜 Exec 失败与新版本起不来。
- 库：三个库只覆盖 rename 那 30~60 行，且都带 #150 已否的东西（回滚 / 签名 / 多平台 SDK）；手写足够。
- 浏览器检测：CORS `*`、匿名 60 次/时/IP、304 豁免仅限已认证、无 release 与 private 都是 404、`assets[].digest` 有 sha256；api.github.com 国内时通时断。
- 国内：jsDelivr 自 2021-12 无 ICP、国内不稳；`@main` 12 小时缓存、`@tag` 永久；只适合镜 `install.sh`。今日活着的 Release 代理：ghfast.top、gh-proxy.com、ghproxy.net（前缀模型），一两年前的推荐已死过半——`PORTAGE_DOWNLOAD_BASE` 按前缀语义定。
