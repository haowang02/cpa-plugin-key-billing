# cpa-key-billing

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 下游 API Key 计费与订阅额度插件。

## 功能特性

- 支持定期重置额度，每个 API Key 独立计时
- 支持按输入 Token 阈值切换长上下文**阶梯计价**
- 可从 [models.dev](https://models.dev/) 获取模型参考价，不用挨个设置模型定价

## 工作原理

插件在请求进入时识别下游 API Key 并检查订阅额度。请求结束后，插件按上游返回的真实 usage 和模型定价计算费用，并更新对应 API Key 的周期消费额。

```mermaid
flowchart TD
    A[下游 API 请求] --> B[识别 API Key<br/>检查订阅额度]
    B --> C{额度是否充足？}
    C -- 否 --> D[返回 HTTP 429]
    C -- 是 --> E[CLIProxyAPI 调用上游模型]
    E --> F[根据 usage 和模型定价<br/>计算 Token 费用]
    F --> G[更新周期消费额]
```

## 环境要求

- CLIProxyAPI `7.2.103` 或更高版本，建议使用最新版本
- 使用支持插件的 CLIProxyAPI 构建，不要使用 `no-plugin` 版本

## 安装

在 CLIProxyAPI 根目录运行：

```sh
curl -LsSf https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.sh | sh
```

安装脚本会识别操作系统和处理器架构，并将插件安装到 `plugins/` 目录。安装完成后需要重启 CLIProxyAPI。

也可以从 [Releases](../../releases/latest) 下载对应平台的发布包，解压后将动态库放入 CLIProxyAPI 的 `plugins/` 目录：

```text
plugins/cpa-key-billing.so       # Linux
plugins/cpa-key-billing.dylib    # macOS（二选一）
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
      log_entries: 200
```

- `enabled`：启用计费和订阅额度控制
- `state_file`：计费状态文件路径
- `log_entries`：保留的计费日志条数；设为 `0` 可关闭日志，最大为 `5000`

重启 CLIProxyAPI 后，在管理中心打开「API Key 计费」。确认模型定价后，创建订阅计划并绑定需要限制的 API Key。

## 计费与订阅规则

- 未绑定订阅计划的 API Key 只统计用量，不限制额度。
- 未定价模型按 `0 USD` 记录，并在计费日志和运行诊断中标记。
- 每个 API Key 在首次使用时独立启动周期。周期到期后回到未开始状态，并在下一次使用时开启新周期。
- 达到订阅额度后，后续请求返回 HTTP `429`。

## 致谢

- [LINUX DO](https://linux.do/)——面向创造者与好奇者的社区
