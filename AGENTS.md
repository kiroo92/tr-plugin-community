# 社群中心正式插件

- 独立 Git 仓库为 https://github.com/kiroo92/tr-plugin-community，主分支 main；只在本目录提交，不包含工作区其它插件。

- 原型见 ../telegram-prototype/index.html；正式实现仅邀请领取、AI 助手和公开渠道状态图。禁止恢复 TG 绑定、套餐发放或入群验证。
- 使用公开 SDK import，版本化 replace 到 kiroo92/TokenRouter-plugin-data/plugin-sdk/go v0.3.0，最低宿主为该分发 v0.7.3。
- 业务数据在 DataDir/plugin.sqlite，Init 强制检查目录可写；StateDir 不保存业务数据。密钥仅取宿主 secret 配置，不写入数据库或前端。
- 重型命令串行并设置 GOMAXPROCS=2、GOFLAGS=-p=1、GOMEMLIMIT=1GiB。
- 任务恢复见 TODO.md；正式运行、备份和限制见 README.md。
- Windows 使用 ./build.ps1 完成测试、前端构建、五平台纯 Go 编译与 SDK 包校验；Go 竞态检测需要额外 C 编译器，结果与未验证范围见 VERIFICATION.md。
- 公共渠道数据通过 Host.Catalog.Models（catalog.read）读取，不抓取宿主私有接口。后台每 5 秒轮询，关闭领取后仍处理旧邀请事件；Shutdown 取消并等待退出后关闭数据库。
