# 业界开源沙盒方案对比

这份文档存在的理由：agenthub 过去自称控制面，却把每一次执行都交给一个它从不约束的外部进程。沙盒改造用一个「提供方目录」取代了这一点，而目录只有在运维能看清每一项究竟买到了什么时才有意义。

对比按**隔离层**组织：这是唯一不会被包装话术稀释的维度——同层的两个产品差在交付形态，不差在一次失陷能触及什么。

## 1. 分档

| # | 层 | 代表项目 | 边界由什么强制 | 启动 | 内核攻击面 | 工具链兼容 | 许可证 | 接入 agenthub 的方式 | 是否内置 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ① | microVM | Firecracker、Cloud Hypervisor、libkrun、crosvm、Kata Containers | 硬件虚拟化 + 客户机内核 | 亚秒~秒（Firecracker ≈125ms） | 极小（不共享宿主内核） | 完整（真内核） | Apache-2.0 | Kata 走 `docker/podman --runtime=kata`；Firecracker 类平台走远端节点协议 | 预设 `kata` |
| ② | 用户态内核 | gVisor（runsc + Sentry + Gofer） | Sentry 在用户态拦截 syscall | ≈100ms | 显著缩小 | 较好（少量 syscall 未实现） | Apache-2.0 | `docker --runtime=runsc`，即 docker 提供方加一个 runtime 参数 | 预设 `gvisor` |
| ③ | 容器 / rootless | Docker、Podman、containerd、runc | namespace + cgroup + capabilities + seccomp/LSM | <1s | 共享宿主内核（真实选项里最弱） | 完整 | Apache-2.0 | 一等提供方 | **`docker`（实例默认）**、`podman` |
| ④ | 进程沙盒 | bubblewrap、nsjail、isolate、systemd-run | user/mount namespace + seccomp + rlimit | 毫秒级 | 共享内核，由 seccomp 收敛 | 完整，无镜像概念 | LGPL-2.0+ / Apache-2.0 | `bwrap` 是一等提供方；nsjail/isolate/systemd-run 是声明式模板 | `bwrap`、受限 `local`（无额外二进制） |
| ⑤ | WebAssembly | Wasmtime、WasmEdge、Wasmer | 字节码验证 + 能力式导入 | 毫秒级 | 极小 | 差（工具链要编成 wasm） | Apache-2.0 | 声明式模板，非主路径 | 否 |
| ⑥ | 语言级 | Pyodide、Deno/Node 权限模型 | 解释器内能力控制 | 毫秒级 | 取决于实现 | 差（仅单一语言） | 多种 | 不接入：它不是 OS 级执行边界 | 否 |
| — | 平台层 | E2B、microsandbox、Daytona、CubeSandbox、OpenHands runtime | 组合上述原语 + SDK | 80ms~秒 | 取决于底座 | 好（SDK） | 以 Apache-2.0 为主 | 远端节点协议，或声明式 HTTP 模板 | 预设，**不内嵌 SDK** |

## 2. 每一档真正买到了什么

**① / ② —— 把敌意代码与宿主内核隔开。** 只有 microVM 或用户态内核适用于「代码被假定为恶意」而不是「代码可能写错」的场景。代价是运维：镜像、运行时安装，microVM 还需要能开 KVM 的宿主。两者都不是单二进制控制面该有的默认值，因此都是显式开启。

**③ —— 默认档。** Docker 是运维手上大概率已经装好的唯一一层。`--network=none --read-only --cap-drop=ALL --security-opt no-new-privileges --pids-limit --memory --cpus -u 65534` 已经把一次提示词注入能做的事压到很小，而且离运行时升级只有一个参数（`--runtime=runsc` / `kata`）。诚实的边界：容器逃逸能摸到宿主内核，所以描述符写的是 `isolation: container`，不写「已沙盒化」。

**④ —— 便宜的本地档。** `bwrap` 毫秒级启动、不需要镜像，这让它成为没有容器运行时的开发机或 CI 机器的正确答案。诚实的边界：没有 seccomp 配置时它是 namespace 边界而非 syscall 边界，agenthub 在目录里如实标注。nsjail 与 isolate 补上 seccomp，两者都能通过声明式模板接入，不必等一次 Go 发版。

**平台层 —— 不要把 SDK 嵌进来。** E2B、microsandbox、Daytona、CubeSandbox、OpenHands 各自拥有一套生命周期词汇（`sandbox` / `machine` / `workspace` / `session`）与客户端库。内嵌任何一个都等于把厂商 SDK 变成 agenthub 的生产依赖，也等于替运维选边。远端节点协议把它们挡在一臂之外：一层薄适配翻译 create/exec/attach/destroy/status，凭据只以环境变量名引用，永不入库。

## 3. 选型规则

1. **默认 ③。** `AGENTHUB_SANDBOX_PROVIDER=docker`；不可用即拒绝（`sandbox_unsupported`），绝不回退到更弱的后端。
2. **代码不可信时升到 ② / ①。** `--runtime=runsc` 改一个词；Kata 同理。完整的 microVM 集群放在远端协议后面。
3. **没有容器运行时降到 ④。** `bwrap`，或 `local`（并明确接受它没有隔离），给笔记本与受限 CI。
4. **`mock` 只用来保持离线。** 单测、`make web-dev`、演示显式指定；它不执行任何东西，并在每次回执里写明。
5. **提供方永不被推断。** 资源声明了自己拿不到的提供方时，在注册阶段就被拒绝，而不是运行期降级。

## 4. 能力声明与准入

`Describe()` 是 manager 在「租约存在之前」就执行的契约：`Network` 列出该提供方**真能施加**的网络模式，
`Requires` 是探针必须找到的二进制，`EnvKeys` 是垫片需要的环境变量，`Source` 说明它是内置、声明式、
远端节点还是平台垫片。`delegated` 是给「把 profile 交给别人的执行点」的那一层的诚实取值——它说的是
边界在谁那里，而不是没有边界。

落到各后端：`docker` 声明 `none|bridge|platform`，并把 `platform` 交给守护进程的默认网络（不再发字面
`bridge`）；`bwrap` 只声明 `none|platform`，因为 `--share-net` 是宿主网络命名空间、它给不出 NAT 接口；
`local` 只声明 `platform`；远端节点、平台垫片与未声明网络的模板一律声明 `delegated`。

`Manager.Acquire` 拒绝「提供方从未声明过的模式」——`sandbox_capability_unavailable`，且提供方一次都
不会被调用。在这条规则出现之前，声明只是装饰：向 bubblewrap 要 `bridge` 会静默拿到宿主的网络。

## 5. agenthub 的位置

agenthub 是**控制面**：决定谁能执行、在哪执行、限额多少、存活多久，并记录发生了什么。它刻意不是沙盒实现本身。提供方目录就是那条缝：新增一档意味着注册一个提供方或写一段模板，而不是改 hub、改适配器、改控制台。

API 与表结构见 `requirements-sandbox.md`，上面引用的模板见 `internal/sandbox/presets.go`。
