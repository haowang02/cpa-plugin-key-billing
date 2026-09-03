<div align="center">
  <h1>CPA Key Billing</h1>
  <p><strong><a href="https://github.com/router-for-me/CLIProxyAPI">CLIProxyAPI</a> 下游 API Key 计费与订阅额度插件。</strong></p>
  <p>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/releases/latest"><img src="https://img.shields.io/github/v/release/haowang02/cpa-plugin-key-billing?label=release" alt="Latest release"></a>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/actions/workflows/check.yml"><img src="https://github.com/haowang02/cpa-plugin-key-billing/actions/workflows/check.yml/badge.svg" alt="CI status"></a>
    <img src="https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue" alt="Platforms: Windows, macOS, and Linux">
    <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT License"></a>
  </p>
</div>
<img src="images/example.png" alt="cpa-plugin-key-billing example" width="100%" />

## 功能特性

- 支持定期重置额度，每个 API Key 独立计时
- 支持按输入 Token 阈值切换长上下文**阶梯计价**
- 支持按 API Key 设置**最大并发请求数**
- 支持为每个 API Key 绑定**路由规则**，限制模型访问范围和上游凭证
- 可从 [models.dev](https://models.dev/) 获取模型参考价

## 工作原理

插件会在请求到达上游前检查模型权限、API Key 最大并发请求数和订阅额度。通过检查的生成请求占用一个并发槽位，并在请求结束后释放。非流式和流式请求遵循相同规则。上游调用结束后，CLIProxyAPI 通过 `usage.handle` 提供用量。插件据此记录请求事件、计算费用并更新周期消费额，不复制或解析上游响应。

```mermaid
flowchart LR
    A[下游 API<br/>请求] --> M{路由允许<br/>的模型？}
    M -- 否 --> N[返回<br/>HTTP 403]
    M -- 是 --> L{API Key 并发<br/>是否饱和？}
    L -- 是 --> R[返回<br/>HTTP 429]
    L -- 否 --> B[检查<br/>订阅额度]
    B --> C{额度是否<br/>充足？}
    C -- 否 --> D[返回<br/>HTTP 429]
    C -- 是 --> U{路由允许<br/>的凭证？}
    U -- 无可用凭证 --> V[返回<br/>HTTP 503]
    U -- 选择或交给 CPA --> E[CLIProxyAPI<br/>调用上游模型]
    E --> X[发布 request.complete<br/>释放并发槽位]
    E --> F[发布<br/>usage.handle]
    F --> H[归一化<br/>Token 用量]
    H --> G[根据模型定价<br/>更新周期消费额]
```

## 环境要求

- CLIProxyAPI `7.2.143` 或更高版本，建议使用最新版本
- 使用支持插件的 CLIProxyAPI 构建，不要使用 no-plugin 版本

## 安装

在 CLIProxyAPI 根目录运行。macOS 和 Linux 使用：

```sh
curl -LsSf https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.sh | sh
```

Windows 请先停止 CLIProxyAPI，再在 PowerShell 中运行：

```powershell
irm https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.ps1 | iex
```

安装脚本会将插件安装到当前目录的 `plugins/`。安装或升级完成后需要重启 CLIProxyAPI。

也可以从 [Releases](../../releases/latest) 下载对应平台的发布包，解压后将动态库放入 CLIProxyAPI 的 `plugins/` 目录：

```text
plugins/cpa-key-billing.so       # Linux
plugins/cpa-key-billing.dylib    # macOS
plugins/cpa-key-billing.dll      # Windows
```

## 配置

在 CLIProxyAPI 配置文件中加入：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-billing:
      enabled: true
      state_file: "plugins/cpa-key-billing-state-v1.db"
```

> [!WARNING]
> v1.0.0 插件不会迁移旧版 JSON 或 SQLite 数据。请使用新的 `state_file`；如果路径指向旧格式，插件会拒绝打开且不会修改原文件。

重启 CLIProxyAPI 后，在管理中心打开「API Key 计费」。确认模型定价后，创建订阅计划并绑定需要限制的 API Key。

## 页面访问

管理员可以从 CLIProxyAPI 管理中心的「API Key 计费」菜单进入，也可以直接打开：

```text
http(s)://<CLIProxyAPI 地址>/v0/resource/plugins/cpa-key-billing/ui
```

普通用户使用自己的 API Key 查询订阅额度和用量时，直接打开：

```text
http(s)://<CLIProxyAPI 地址>/v0/resource/plugins/cpa-key-billing/ui#account
```

## 计费与订阅规则

- 未绑定订阅计划的 API Key 只统计用量，不限制额度。
- 每个 API Key 独立计算订阅周期。
- 未定价模型按 `0 USD` 记录。
- 请求事件只保留最近 30 天。

## 路由规则

- 在 API Key 页面可直接选择路由规则、整类凭证、单个凭证或单个模型；多个选择取并集。
- 默认不限制模型和凭证。请求会先检查模型权限，再从允许的凭证中选择上游。

## 拦截请求的响应

被拦截的请求按 CLIProxyAPI 自身的错误格式返回，客户端 SDK 无需区分是代理还是插件拒绝了请求：

| 场景 | 状态码 | `type` | `code` |
| --- | --- | --- | --- |
| API Key 并发已满 | `429` | `rate_limit_error` | `rate_limit_exceeded` |
| 订阅额度用尽 | `429` | `rate_limit_error` | `rate_limit_exceeded` |
| 模型无权访问 | `403` | `permission_error` | `insufficient_quota` |
| 没有符合规则且可用的凭证 | `503` | `server_error` | `internal_server_error` |
| 已绑定的路由规则不存在或损坏 | `503` | `server_error` | `routing_configuration_error` |

## 致谢

- [LINUX DO](https://linux.do/) - 新的理想型社区
