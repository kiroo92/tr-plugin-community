# 社群中心正式插件

## 目标与边界
- 按 telegram-prototype 实现邀请领取、AI 助手、渠道状态图；不绑定 TG，不发套餐，不做入群验证。
- 使用 kiroo92 SDK v0.3.0，目标其宿主 v0.7.3；不改宿主，不部署。源码按用户要求发布到独立 GitHub 仓库。
- 重型检查串行，GOMAXPROCS=2、GOFLAGS=-p=1、GOMEMLIMIT=1GiB。

## 验收与步骤
- [x] T1：已核对 SDK README 和 Catalog.Models 公共能力，go list 确认 replace 为 fork v0.3.0。
- [x] T2：DataDir SQLite、Shutdown、邀请和重开/隔离测试已通过。
- [x] T3：主动触发、预算、PNG、冷却及权限回归已通过；外部服务采用本地 HTTP fixture。
- [x] T4：正式 Vue 用户页及四页管理端完成，类型检查/生产构建、浏览器交互、360/390 手机与深色检查通过，截图在 verification。
- [x] T5：用户要求的测试、编译和五平台打包校验完成；额外竞态检查已尝试但缺 gcc，明确记录未通过。交付源码与安装包，不部署。
- [x] T6：v0.1.0 自建签名索引及五平台 GitHub Release 已公开；草稿下载核对所有包大小/哈希、manifest 哈希、Ed25519 签名与篡改拒绝，固定 latest 来源匿名下载及验签通过。

- [x] T7：v0.2.0 靛紫配色与分项入群资格发布。Go 测试 9 组、vet、前端类型检查与构建、五平台打包及 validate 通过；草稿 11 资产逐一核对后公开为 latest，匿名 latest 索引验签与安装包哈希一致。业务规则未变，可直接覆盖升级。

- [x] T8：v0.2.1 支持公开超级群组与群级入群审批。依据 Bot API join_by_request 语义移除「必须无公开用户名」限制，新增 chat_join_request 拒绝路径与测试；五平台打包、草稿核对、签名与匿名 latest 验证通过。

## 恢复点
- 当前：T1–T5 的实现、用户要求的验证与交付文件准备完成。
- 已验证：八组 Go 测试、go vet、SDK 来源、前端类型检查及构建、浏览器交互与主题布局。
- 竞态限制：CGO_ENABLED=1 后仍失败，本机 PATH 无 gcc，尚未完成 -race；普通纯 Go 构建不受影响。
- 结果：build.ps1 再次通过；五个包 validate 与 SHA256 核对通过，详见 VERIFICATION.md。
- 后续验收范围：真实宿主/Telegram/模型联调及有 C 编译器环境的 -race；本次未部署。
