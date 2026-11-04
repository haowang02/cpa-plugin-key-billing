<div align="center">
  <h1>CPA Key Billing</h1>
  <p><strong><a href="https://github.com/router-for-me/CLIProxyAPI">CLIProxyAPI</a> 下游 API Key 计费、额度与路由管理插件。</strong></p>
  <p>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/releases/latest"><img src="https://img.shields.io/github/v/release/haowang02/cpa-plugin-key-billing?label=release" alt="Latest release"></a>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/releases"><img src="https://img.shields.io/github/downloads/haowang02/cpa-plugin-key-billing/total" alt="Total downloads"></a>
    <img src="https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue" alt="Platforms: Windows, macOS, and Linux">
    <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT License"></a>
  </p>
  <p><a href="./README.en.md">English</a> · <strong>简体中文</strong></p>
</div>
<img src="images/example.png" alt="cpa-plugin-key-billing example" width="100%" />

## 功能特性

- 按 API Key 限制金额、Token、请求次数和并发，支持多个 Key 共享额度
- 支持美元、人民币定价和长上下文阶梯价格，可使用 [models.dev](https://models.dev/) 参考价
- 通过路由规则控制可用模型和上游凭证
- 提供管理页面与 API Key 自助查询，保留各 Key 的独立用量统计

## 安装

需要支持插件的 CLIProxyAPI **7.3.7 或更高版本**，不支持 `no-plugin` 构建。

在 CPAMC 或 CPAMP 的插件商店搜索 `cpa-key-billing` 安装，也可在 CLIProxyAPI 根目录运行安装脚本。

macOS / Linux：

```sh
curl -LsSf https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.sh | sh
```

Windows：先停止 CLIProxyAPI，再在 PowerShell 中运行：

```powershell
irm https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.ps1 | iex
```

脚本会将插件安装到当前目录的 `plugins/`。

## 配置

在 CLIProxyAPI 配置文件中加入：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-billing:
      enabled: true
      priority: 100 # 调度优先级，数值越大越优先
      state_file: "plugins/cpa-key-billing-state-v1.db"
      debug: false # 调试日志
      codex_fast_mode_billing: false # Codex priority 请求按 2.5 倍计费
      allow_unpriced_models: false # 允许未定价模型，以零费用记录用量
      cny_per_usd: 7 # 1 美元兑换的人民币数
      request_event_retention_days: 365 # 历史保留天数；缩短会删除超期数据
      mask_api_key_view_emails: false # 隐藏 API Key 查询页的完整邮箱
      allow_api_key_quota_reset: false # 允许用户重置可访问的 Codex / Claude 认证文件额度，消耗上游重置次数
      routing:
        strategy: "round-robin" # round-robin、weighted-round-robin、fill-first
        session-affinity: false # 同一会话优先复用可用凭证
        session-affinity-ttl: "1h" # 会话绑定的空闲过期时间
```

`routing` 仅用于限制了上游凭证的请求，其他请求由 CPA 调度。

安装或升级后重启 CLIProxyAPI。

> [!WARNING]
> 升级前备份 `state_file`。v1.0.0 起的数据库支持自动迁移；v0.8.4 及更早版本的数据不兼容，需改用新的数据文件。

## 页面访问

可从管理面板进入，也可直接访问：

```text
http(s)://<CLIProxyAPI 地址>/v0/resource/plugins/cpa-key-billing/ui
```

## 计费与额度

### 模型价格

自定义价优先于参考价，未定价的模型默认拒绝请求。订阅额度的消费按计划币种及配置汇率换算，历史金额保留原始币种。

### 订阅计划

每个计划可设置多个周期窗口，组合限制金额、Token 和请求次数。未绑定计划的 Key 不受订阅额度限制。

| 类型 | 额度归属 | 周期重置 |
| --- | --- | --- |
| 独立周期 | 每个 Key 独享 | 从各自首次放行请求开始计时 |
| 统一周期 | 每个 Key 独享 | 按计划指定时间统一重置 |
| 共享额度 | 所有绑定 Key 共用 | 按计划指定时间统一重置 |

清理历史记录不重置额度；共享池不会因成员 Key 解绑或删除而退回已用额度。

## 路由规则

- 绑定规则与 Key 的直接设置合并生效：白名单取并集，黑名单优先；白名单为空时仅排除黑名单。
- 整类凭证的规则自动覆盖后续新增的凭证。

CLIProxyAPI **8.0.37 起**，客户端模型列表会按路由权限过滤；旧版本显示完整列表，调用时仍执行权限检查。

## 请求被拒绝时

| 状态码 | 检查项 |
| --- | --- |
| `403` | Key 的路由规则是否允许该模型 |
| `429` | 订阅额度是否耗尽，或并发是否已满 |
| `503` | 模型是否已定价、允许的上游凭证是否可用；具体原因见插件日志 |

## 致谢

- [LINUX DO](https://linux.do/) — 新的理想型社区
