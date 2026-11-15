# v0.1.0

首次发布社群中心，目标为 kiroo92/TokenRouter-plugin-data 分发的 TokenRouter v0.7.3+。

- 符合 LinuxDO 绑定或已完成正金额充值条件后领取限一人邀请；无需绑定 TG。
- Telegram 主动触发 AI 助手、上下文及预算限制。
- 公开渠道 PNG 状态播报，复用已有群消息。
- 使用 Go SDK v0.3.0 和 DataDir SQLite 持久化。
- 发布五平台安装包及自建 Ed25519 签名索引，来源 URL 与公钥见 README。

邀请不限定 TG 身份，人数限制及事件撤销无法保证绝对一次性。未做真实宿主、Telegram 或模型联调；本机缺少 gcc，竞态检测未完成。详见 VERIFICATION.md。
