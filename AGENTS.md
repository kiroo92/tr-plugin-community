# 社群中心正式插件

- 独立 Git 仓库为 https://github.com/kiroo92/tr-plugin-community，主分支 main；只在本目录提交，不包含工作区其它插件。
- 插件标识为 moonup.community，用户入口 /ext/moonup.community，管理入口 /admin/ext/moonup.community。plugin.json 与前端构建的 pluginId 保持一致。
- 自建分发沿用福利插件方式：GitHub Releases latest/download/index.json 加独立 Ed25519 签名，公钥见 README；五平台安装包使用固定版本 URL。完整草稿下载验签及哈希核对后公开，不替换同版本资产。私钥在用户配置目录 TokenRouter/plugin-signing/tr-plugin-community.key，禁止进入项目或发行包，需由维护者备份。

- 原型见 ../telegram-prototype/index.html；正式实现为永久机器人入口、Start 自动绑定、入群审批、AI 助手和公开渠道状态图。每个网站账号与一个 Telegram 账号绑定，不发放套餐。
- 配色：正式前端采用原型选定的靛紫方案，主色 #4f46e5（深色 #a5b4fc），定义在 style.css 的 .community 与 .community.cm-dark。作用域内把 --tr-color-brand/--tr-color-accent 指向自定义主色，使宿主 TrButton、复选框和焦点环统一；主操作按钮用自绘 .cm-primary。不要给 .community button 设 color:inherit，前缀选择器特异性会覆盖宿主按钮文字色。资格区按 /status 的 linuxdo、recharge（null=未核验）分项渲染，避免把「无需核验」误写成未满足。
- 使用公开 SDK import，版本化 replace 到 kiroo92/TokenRouter-plugin-data/plugin-sdk/go v0.3.0，最低宿主为该分发 v0.7.3。
- 业务数据在 DataDir/plugin.sqlite，Init 强制检查目录可写；StateDir 不保存业务数据。密钥仅取宿主 secret 配置，不写入数据库或前端。
- 重型命令串行并设置 GOMAXPROCS=2、GOFLAGS=-p=1、GOMEMLIMIT=1GiB。
- 任务恢复见 TODO.md；正式运行、备份和限制见 README.md。
- Windows 使用 ./build.ps1 完成测试、前端构建、五平台纯 Go 编译与 SDK 包校验；Go 竞态检测需要额外 C 编译器，结果与未验证范围见 VERIFICATION.md。
- 群组支持公开或私有超级群组。网站领取永久 `https://t.me/<bot>?start=<随机码>` 链接，不显示倒计时。私聊 Start 首次使用时绑定 Telegram ID，绑定持久化后才私发群邀请；同一 Telegram ID 不得绑定其他网站账号。审批按申请人的 Telegram ID 查绑定并复核网站资格，可处理不带 invite_link 的入群申请。机器人入口与群邀请均不设有效期。群邀请使用 creates_join_request=true，省略 expire_date 和 member_limit；批准前持久化已使用，结果不明时保留锁定，明确拒绝时回滚。公开群须开启群级审批。入口 token 不写日志或管理列表，只能出现在本人领取的机器人 URL 中。
- 状态图由 channel_image.go 使用内置 OFL 字体渲染，显示中文分组和实际历史桶。账号数量通过 pool_counts.go 读取透明号池的可选公开统计，并按本地分组 ID 对应；只在图片附文末尾列 GPT 满血与特惠，图片不显示数量。未知与零区分，独立显示号池快照时间，失败清空数量但继续生成当前渠道图。
- 公共渠道数据通过 Host.Catalog.Models（catalog.read）读取，不抓取宿主私有接口。后台每 5 秒轮询，关闭领取后仍处理旧邀请事件；Shutdown 取消并等待退出后关闭数据库。
- 命令入口为 commands.go，由 poll 在 AI 处理前路由。群里回复用户消息后 `/id` 查询回复对象；sender_chat 优先于匿名消息的合成 From。私聊 `/status` 和 `/invite` 使用发送者绑定，群状态只展示公共开关，`/reset` 只删除发送者自己的 AI 历史。连接检查同步私聊与目标群命令菜单，两个作用域均成功后才保存内容指纹，菜单失败记事件并保留后续重试。

- GET /binding 为 public 路由，独立密钥取宿主 secret binding_api_key（至少 32 字符），请求头 X-Plugin-Admin-Key。认证后查询当前作用域的 TelegramID，200 只返回纯文本是/否；API 错误须用非 200 状态，不能返回否。不要改用 Authorization，该头不会透传到插件。
