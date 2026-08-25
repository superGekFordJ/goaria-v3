<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="frontend/src/assets/icons/dark.svg">
    <img src="frontend/src/assets/icons/light.svg" width="128" height="128" alt="GoAria Icon" />
  </picture>
  <h1>GoAria v3</h1>
  <p>
    <strong>原生双引擎架构 · 智能线程预测 · 液态玻璃美学</strong>
  </p>
  <p>
    以自适应链路感知算法推演最优并发，兼具极致性能与零配置负担。
  </p>

  <p>
    <a href="https://golang.org/">
      <img src="https://img.shields.io/badge/Language-Go-blue.svg" alt="Go">
    </a>
    <a href="https://wails.io">
      <img src="https://img.shields.io/badge/Framework-Wails%20v3-red.svg" alt="Wails">
    </a>
    <a href="https://vuejs.org/">
      <img src="https://img.shields.io/badge/Frontend-Vue%203-green.svg" alt="Vue">
    </a>
    <a href="https://tailwindcss.com/">
      <img src="https://img.shields.io/badge/Style-Tailwind%20v4-38bdf8.svg" alt="Tailwind">
    </a>
  </p>

  <p>
    <a href="https://addons.mozilla.org/addon/goaria">
      <img src="https://raw.githubusercontent.com/superGekFordJ/goaria-assets/main/get-the-addon.svg" alt="Get the Add-on" height="52">
    </a>
    <a href="https://microsoftedge.microsoft.com/addons/detail/goaria/ofjjbleopjcflpbdnklpdnchbmkkicfd">
      <img src="https://raw.githubusercontent.com/superGekFordJ/goaria-assets/main/get-it-from-ms-edge.svg" alt="Get it from Microsoft Edge" height="52">
    </a>
  </p>

  <p>
    <a href="./README.md">简体中文</a> | <a href="./README_EN.md">English</a>
  </p>
</div>

## 📖 简介

**GoAria** 是基于 [Wails v3](https://wails.io) 构建的现代化极速下载器。从 v3.0 开始，GoAria 迎来双引擎架构升级：在保留 `aria2c` 作为后备引擎与标准 RPC 接口的同时，深度集成了专为 HTTP(S) 打造的内置原生 `surge` 极速核心，并引入了**自适应线程预测机制**。

GoAria 秉持**实用主义**与**零干扰**的设计哲学。传统的固定线程配置往往在网络拥塞与吞吐不足之间妥协，而 GoAria 通过持续记录与分析历史测速数据及服务器响应特征，能够在任务创建时毫秒级推演最优并发数与分块策略，在充分释放带宽潜能的同时有效避免触发服务器频控限制。配合液态玻璃质感的原生界面与低资源占用，提供纯净、高效的现代下载体验。

<div align="center">
  <img src="frontend/src/assets/images/display.png" alt="GoAria Screenshot" width="800" />
  <p><em>深色模式：黑曜石与激光的视觉交响</em></p>
</div>

## ✨ 核心特性

### 🧠 智能自适应线程调度

- **链路感知并发预测**: 基于历史测速样本与目标主机响应特征，在任务初始化阶段毫秒级计算最优线程数与切片粒度，无需手动配置即可实现吞吐最大化。
- **原生双引擎协同**: HTTP(S) 传输默认由进程内原生 `surge` 核心直驱，消除 IPC 开销；`aria2c` 进程作为可靠后备与生态 RPC 交互接口。
- **单线程与高并发性能跃升**: 结合分级内存池机制与纯事件驱动设计，显著提升单线程传输效率，轻松跑满千兆及 2.5Gbps 物理带宽。
- **内网极速预分配**: 针对 Windows 重构预分配机制，瞬间打满 2.5Gbps 物理网卡上限。
- **智能组下载**: 批量添加的任务自动归档至专属文件夹，并在任务列表中聚合为单一“组卡片”，支持一键全组控制与后台清理。

### 🚀 极致轻量

- **低资源占用**: 基于 Go 与 Wails v3，底层开销极低。
- **内嵌 Surge 原生引擎**: 采用内嵌编译与零 IPC 进程损耗设计，彻底告别传统跨进程通信造成的性能与上下文切换瓶颈。
- **真·纯后台极低驻留**: 支持最小化/托盘纯后台静默运行，常规后台待机内存占用压缩至约 **20MB**。
- **纯粹事件驱动与零无效唤醒**: 全面采用事件信使推送机制，告别传统高频轮询。无 Aria2 任务时自动休眠，仅在状态真实变动时按需通知，彻底消除待机功耗。

### 🧊 现代液态玻璃美学

- **原生沉浸**: 深度适配 Windows 11 Mica / Acrylic 材质，支持自定义皮肤（黑曜石与激光 / 电子纸与陶瓷）。
- **液态玻璃特效**: 统一的高级“液态玻璃”视觉组件，提供更灵动且具质感的交互反馈，并智能响应系统“减弱动态效果”模式以节能。
- **智慧交互**: 灵感源自 Spotlight 的一键抓取设计，通过动态光效而非枯燥文字传达任务状态。
- **极致流畅**: 虚拟滚动技术加持，处理上千个任务依旧丝滑无感。
- **官方浏览器扩展**: 首个无缝接管浏览器请求的扩展即将上线，为您提供极致顺滑的下载接力体验。

## 🛠️ 技术架构

GoAria 采用 **Frontend-Backend-Daemon** 三层架构，确保稳定性与扩展性。

```mermaid
graph TD
    A["Frontend UI (Vue 3 + Pinia)"] <-->|Single Source Event Bus| B["Backend (Go App)"]
    B <-->|RPC & WS Sensor| C["Daemon (Aria2c Fallback)"]
    B -->|Event Bridge| D["In-Process Engine (Surge)"]
```

- **Frontend**: 负责极致的液态玻璃 UI/UX 呈现。
- **Backend**: 作为中央 Hub 聚合来自 Surge 与 Aria2 的状态，通过单源信使（Single Source Event Bus）将事件统一推送至前端。
- **Dual Engines**: `surge` 作为内置极速 HTTP(S) 引擎；`aria2c` 作为独立进程提供跨协议兼容与原生 RPC 接口。

## 💻 开发指南

确保您的系统已安装 Go 1.26+ 和 Node.js 22+。

### ⚡ 快速设置

我们提供了自动化脚本来快速准备开发环境（检查工具链、安装依赖并准备 Aria2 内核等）：

**Windows (PowerShell):**

```powershell
powershell -ExecutionPolicy Bypass -File .\setup.ps1
```

**Linux / macOS (Bash):**

```bash
chmod +x setup.sh
./setup.sh
```

### 手动设置

```bash
# 克隆项目
git clone https://github.com/superGekFordJ/goaria-v3.git
cd goaria-v3

# 我们推荐使用 pnpm，如果没有安装的话
# cd frontend
# corepack enable
# corepack prepare pnpm@latest --activate
# corepack use pnpm

# 安装依赖
wails3 task deps:update:frontend
wails3 task deps:update:go


# 启动开发模式 (支持热重载)
wails3 dev

# 构建生产版本
wails3 task build

# 如需打包产物
wails3 task package
```

> Linux / macOS 构建说明：发布产物现在同时内置了 `surge` 引擎与 `aria2c`。本地构建/打包流程仍会强制准备可嵌入的 `aria2c`。原生构建器可直接安装系统 `aria2c` 后运行 `wails3 task linux:prepare:aria2` 或 `wails3 task darwin:prepare:aria2`。

## 📄 许可证

[MIT License](LICENSE)
