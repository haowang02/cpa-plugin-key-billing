# cpa-key-billing

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的下游 API Key 计费与周期额度插件。

## 功能

- 按模型统计费用
- 为 API Key 设置日、周、月或自定义周期额度
- 查看用量、消费和计费日志
- 自动同步 API Key 和模型列表

## 要求

- CLIProxyAPI `v7.2.97` 或更高版本
- 使用支持插件的 CLIProxyAPI 构建，不要使用 `no-plugin` 版本

## 安装

从 [Releases](../../releases/latest) 下载对应平台的发布包：

| 系统 | 架构 | 发布包 |
| --- | --- | --- |
| macOS | Intel | `cpa-key-billing_darwin_amd64.tar.gz` |
| macOS | Apple 芯片 | `cpa-key-billing_darwin_arm64.tar.gz` |
| Linux | amd64 | `cpa-key-billing_linux_amd64.tar.gz` |
| Linux | arm64 | `cpa-key-billing_linux_arm64.tar.gz` |

解压并将插件放到 `plugins/` 目录：

```text
plugins/
└── cpa-key-billing.so
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
      state_file: "plugins/cpa-key-billing-state.json"
      default_timezone: "Asia/Shanghai"
      log_entries: 200
```

- `state_file`：计费状态文件
- `default_timezone`：订阅周期使用的 IANA 时区，默认为 `UTC`
- `log_entries`：保留的计费记录数，设为 `0` 可关闭，最大为 `5000`

重启 CLIProxyAPI 后，在管理中心打开「API Key 计费」。

## 使用

1. 在「模型价格」中确认或设置模型价格。
2. 创建订阅计划并设置周期额度。
3. 为需要限制的 API Key 绑定订阅计划。

## 规则

- 未绑定计划的 API Key 只统计用量，不限制额度。
- 未定价模型按 `0 USD` 计费。
- 日计划每天重置，周计划每周一重置，月计划每月 1 日重置。
- 达到额度后请求返回 HTTP `429`，进入新周期后自动恢复。
