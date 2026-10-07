# Paca task check-in plugin

独立、开源的 Paca v0.18.6 拍照打卡插件。官方 Paca 保持原版；本插件通过官方 REST API 更新任务。

开发中。生产启用须先通过对应 Action、部署检查及用户真机验收；尚未通过的能力不会标为交付。

## 安全与构建

所有编译、lint、测试和镜像构建在 GitHub Actions。公共仓库／镜像不包含真实凭据。worker 使用自己的 schema 和受限 API key，照片存私有对象前缀，短期链接只授权单实例／卡次。默认各提前十分钟，截止严格，独立两卡，开始进行中／结束完成，照片保留九十天。

通用动作采用 PushGo metadata.action_version=1 / action_kind=web / action_url / action_label；队列插件独立，未启用打卡时保持普通提醒。

部分插件控制和 AES-GCM 辅助代码沿用 Self-Command/paca-plugin-tasknotes-webhook；其 crypto helpers 源于 Apache-2.0 Paca-AI/paca-plugin-webhook v0.2.0。保留 LICENSE 和来源说明。
