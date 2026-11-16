# TokenRouter 社群中心

源码仓库：https://github.com/kiroo92/tr-plugin-community（main）。

根据 ../telegram-prototype 实现的独立插件。包含网站永久机器人入口、Start 自动绑定与入群审批、主动触发 AI 群聊，以及公开渠道 PNG 状态播报。

## 兼容与安装

推荐添加自建插件来源，启用后刷新发现页并安装「社群中心」。

当前插件标识为 `moonup.community`，用户入口为 `/ext/moonup.community`。从旧标识版本切换时，先停用旧插件，再安装新项并填写配置；宿主为新标识创建独立数据目录。

- 名称：社群中心 · kiroo92
- 来源 URL：`https://github.com/kiroo92/tr-plugin-community/releases/latest/download/index.json`
- Ed25519 公钥：`R1zeOnAlclBZjK8RPlPqFh7wZzeiD/WTk2OjnXHJ8S0=`
- 发布页：https://github.com/kiroo92/tr-plugin-community/releases

从 v0.2.3 起使用上述新公钥。已有来源需先在宿主的插件来源设置中替换公钥，再刷新索引并更新；来源 URL 保持相同。

索引附带独立的 index.json.sig；安装包使用固定版本下载地址并校验 SHA256。发布时先上传完整草稿、下载核对后公开；同版本资产不替换，更新应递增插件版本。

- 目标是 kiroo92/TokenRouter-plugin-data 分发的 TokenRouter v0.7.3 及以上；其它分发的相同版本号不代表支持 DataDir。
- Go import 保留 github.com/TokenFlux/TokenRouter/plugin-sdk/go，go.mod require v0.2.1，通过版本化 replace 选择 github.com/kiroo92/TokenRouter-plugin-data/plugin-sdk/go v0.3.0。使用 go list -m -json github.com/TokenFlux/TokenRouter/plugin-sdk/go 核对 Replace；go.sum 一同保留。
- Web SDK 固定 0.2.0，不因 DataDir 升级。打包工具统一为 go tool trplugin，不使用另一个带旧版本号的 go run。
- 也可从 GitHub Release 中选择宿主平台对应的 tar.gz，通过宿主本地安装入口安装。此项目不自动部署、不上传官方市场。
- 接受 user.read、user.identities、payment.read、catalog.read 权限。

## 设置与运行

1. 创建专用 Telegram 机器人，加入目标超级群组并授予管理员及邀请用户权限。公开群与私有群都支持。不要与其它轮询程序共用；已有 Webhook 时检查会报错，插件不会替你删除。
   - **公开群必须先开启群级「入群审批」**，否则任何人用 `@用户名` 就能直接进群，资格校验形同虚设。
   - 网站生成机器人专属链接。用户私聊点击 Start 后自动绑定网站账号与 Telegram ID，机器人再私发入群邀请。申请入群时按 Telegram ID 查绑定并核验资格，公开群回调没有 `invite_link` 时也能审批。
   - 公开群的门禁由群级审批与链接审批共同组成。机器人需要 `can_invite_users` 才能批准或拒绝。
2. 在宿主插件原生配置填写 bot_token、chat_id（-100 开头）和可选 ai_key。密钥由宿主 secret 存储，不进入 SQLite、前端或运行事件。
3. 社群管理的邀请页点击检查连接，确认后开放领取。用户在网站领取专属机器人链接，打开后点击 Start 完成绑定，再使用机器人私发的群邀请申请入群。AI 页填写兼容 Chat Completions 的 HTTPS /v1 基础地址、模型、人设与限制，再开启助手。仅 localhost/127.0.0.1/::1 允许 HTTP。
4. 渠道页开启播报，配置 30/60/120 分钟或仅手动。消息需完整匹配查询词，AI 别名按消息开头匹配；@机器人、回复此机器人可分别启停。机器人管理员可收到群消息，但仅主动触发的正文和该成员的有限上下文被送给模型。
5. 机器人每 5 秒轮询一次，每 5 分钟复核连接及权限。处理指定群组的命令、AI 消息及机器人私聊命令；群消息超过 2 分钟不补发，入群事件和待完成的私聊命令仍处理。关闭领取会暂停新入口和 Start，已绑定用户的入群申请仍核验。卸载/停用进程才停止后台工作。

所有功能默认关闭。公共设置保存在插件数据库；密钥和目标群组数字 ID 在宿主原生配置。切换机器人 ID 或群 ID 会进入独立业务命名空间；设置机器人权限和群规则仍由管理员负责。

## 机器人入口与入群规则

- 当前网站账号须 active，且绑定 LinuxDO，或有正金额、COMPLETED、已有支付时间的 balance 充值订单。套餐订单不计入；订单最多检查 5000 条，超出明确报错。
- 网站返回 `https://t.me/<机器人用户名>?start=<随机专属码>`。每个机器人/群组作用域为网站账号保存一条入口，长期有效，重复领取和进程重启后继续复用，页面不显示倒计时。
- 首次私聊 Start 将此入口绑定到发送者 Telegram ID；网站账号和 Telegram 账号在该作用域一一对应。已绑定的入口被其他人打开时拒绝改绑，已绑定的 Telegram 账号也不能领取另一个网站账号的资格。首次绑定前请保管好专属链接。
- 领取、Start 和入群审批时核验网站资格。Start 绑定成功后机器人私发需审批的群邀请；群邀请采用 `creates_join_request=true`，最多自动批准绑定的一个账号。Telegram 仍需用户点击一次群邀请并提交加入申请。
- 入群申请按 Telegram ID 查绑定，允许 Telegram 缺少邀请来源字段。未绑定、入口已撤销、资格失效或资格已使用的申请会被拒绝。申请人使用哪条群链接不会改变其绑定的网站账号。
- 批准前持久化已使用标记，再调用 Telegram 并撤销已使用的群邀请。明确 4xx 拒绝允许重试；网络中断、5xx 或响应无效时保留使用标记并记录待核实，避免结果不明时重复放行。
- 机器人专属入口和机器人私发的群邀请均不设有效期。重复 Start 复用尚未使用的群邀请，入群后主动撤销；旧版带有效期的群邀请在再次 Start 时撤销并换成不过期邀请。创建结果不明时短暂延迟 API 重试，入口和群邀请不会因此产生到期时间。
- 管理员撤销入口会使原专属码失效，并撤销尚可用的群邀请；绑定关系继续保留。用户重新领取会生成新的专属码，仍由已绑定的 Telegram 账号使用。
- 公开群须开启群级审批，机器人须有邀请用户权限。管理员和其他机器人的批准由群组管理者负责。关闭领取会停止新链接和 Start 处理，已发起的绑定账号入群申请仍按资格核验。
- 绑定、专属码和群邀请保存在插件 SQLite；网站只展示本人的机器人 URL 与绑定 ID，管理列表展示绑定状态和 ID，隐藏专属码及群邀请。升级后已有普通群邀请用户需回网站领取机器人入口再完成 Start 绑定。

## 机器人命令

在群里回复要查询的用户消息，再发送 `/id`，机器人会返回对方的 Telegram 用户 ID 和当前群 ID。直接发送 `/id` 查询自己；也支持 `/id@机器人用户名`。匿名管理员或频道身份发出的消息只返回发送方聊天 ID，并说明 Telegram 未提供个人 ID。

| 命令 | 私聊 | 指定交流群 |
| --- | --- | --- |
| `/start` | 从网站专属链接首次绑定，或继续获取已绑定账号的群邀请 | 提示前往网站及私聊绑定 |
| `/help` | 查看私聊命令与绑定说明 | 查看群命令和回复查 ID 的用法 |
| `/id` | 查询 Telegram ID | 查询本人或被回复消息的发送者 ID，同时显示群 ID |
| `/status` | 查看本人的网站绑定、当前资格和入群资格使用状态 | 查看邀请、AI 助手及渠道播报开关 |
| `/invite` | 重新发送本人的永久群邀请，首次绑定须从网站专属入口完成 | 提示前往私聊 |
| `/channels` | 提示前往交流群 | 更新公开渠道状态图，遵守播报开关与查询冷却 |
| `/reset` | 清除本人在当前群的 AI 对话上下文 | 清除发送者本人的上下文，回复他人消息也只清除自己 |

连接检查成功后自动注册私聊和目标群的命令菜单。菜单内容未变时复用已保存的同步记录；更新失败会记录事件，并在后续连接检查时重试，群命令和入群处理仍可运行。`/id`、`/help` 等基础命令在邀请和 AI 关闭时可用。

群内回复保留命令所在的话题。绑定详情和网站账号 ID 通过私聊 `/status` 查询；群内 `/status` 显示公共运行状态。发给其他机器人的命令会被跳过，命令正文不作为 AI 提问。

## 管理员绑定查询 API

在宿主的社群中心原生配置中填写「绑定查询独立密钥」，使用至少 32 字符的随机值。该 secret 与网站登录、机器人 Token 独立；留空时接口关闭。

```bash
curl --get 'https://moonup.dev/api/v1/plugins/moonup.community/api/binding' \
  --header "X-Plugin-Admin-Key: ${COMMUNITY_BINDING_KEY}" \
  --data-urlencode 'user_id=123'
```

成功时 HTTP 200，正文为纯文本 `是` 或 `否`。查询当前机器人和群组作用域内的网站用户 ID；已完成 Start 绑定返回 `是`，没有绑定记录返回 `否`。已撤销入口或已使用入群资格仍保留绑定，继续返回 `是`。

鉴权失败返回 401，用户 ID 无效返回 400，处理繁忙返回 409，未配置密钥或插件不可用返回 503，存储读取失败返回 500。错误不返回 `否`，调用方先检查 HTTP 状态。使用 `X-Plugin-Admin-Key`，宿主会把该头转交插件；密钥放在请求头中，成功响应不含 Telegram ID 或网站账号详情。

## AI 与渠道

- 每位群成员独立历史，最多 30 轮，每条问题最多 1000 字符；历史请求正文限制约 24 KB。回复以纯文本发送，最多保留 4000 字符。
- 每人每分钟限流；群每日 tokens 预算按 UTC+8 重置。调用前按输入字节数、消息开销和最大回复预留，成功且返回 usage.total_tokens 时用实际值校正。失败、超时或无 usage 保留预留额；这是保护预算的保守估计，不是账单结算器。模型供应方可能采用不同计费规则。
- 调用前推进 Telegram offset，避免同一更新在进程重启后重复调用；发送中崩溃可能漏回复。无法承诺 Telegram 与模型之间的恰好一次交付。
- 渠道观测通过 SDK Host.Catalog.Models 读取公开目录，最多显示前 12 个公开分组。目录读取失败保留最后成功图片和时间。历史色块按 ≥99.5%、≥98%、≥90%、<90% 分档；缺失采样后可用率时显示灰色，不从原始请求计数补算。
- PNG 在后端生成，采用深色标题栏、浅色列表和中文分组名。历史色块使用宿主实际时间桶与采样后的可用率，展示窗口和粒度随公开数据变化；无有效观测显示灰色。首字 P50 与缓存命中分别取最近有效桶，缺少桶观测时使用窗口汇总。中文字体随二进制内置，跨平台不依赖系统字体。
- 可用账号只在群消息附文末尾以 `[GPT满血] 可用账号：563`、`[GPT特惠] 可用账号：170` 两行展示，图片内和其他分组不显示账号数量。统计按本地分组 ID 对应，再使用名称中的 GPT 与满血/特惠确定这两类文字标签；名称不参与数量关联。
- 号池数量需要「透明号池 / PoolScope」v0.1.3+。在透明号池的宿主原生配置开启「公开分组的可用账号统计」；社群中心原生配置的「透明号池公开统计地址」填写 `https://<本站域名>/api/v1/plugins/poolscope.account-pool/api/public/group-stats`，本项目默认使用 moonup.dev。留空关闭读取。接口只返回公开分组的可用数量及快照时间，不需要网站登录令牌；普通账号、凭据、总账号与并发不进入附文。
- 可用数量采用透明号池完整快照的 `status=active` 口径，包含当前占满并发的账号，不等于此刻可调度数量。快照有来源错误或超过 5 分钟时标记旧快照；请求失败、分组未绑定或统计不可用时显示“暂无数据”，不会沿用旧数量冒充新观测，也不会填 0。
- 首次 sendPhoto，之后 editMessageMedia 更新同一条消息。查询冷却限制群内刷新；发送失败保留尝试时间，避免每 5 秒反复发送。原消息被删除或失去编辑权限时会报错，不自动重复发新消息。
- 模型对话正文以明文保存在插件数据库，管理员可清理当前群的上下文；运行事件只保留最近 20 条摘要。不要在群里发送密钥或隐私信息。

## 数据、备份与生命周期

- 必须使用宿主提供的非空绝对 DataDir，Init 检查目录、写入和数据库打开；不可用即失败，不回退到当前目录或 StateDir。
- 唯一业务文件为 filepath.Join(env.DataDir, "plugin.sqlite")，SQLite 使用 WAL（运行中可能有 -wal/-shm）。邀请、使用状态、offset、历史、预算、图片及设置都持久化；StateDir 不存业务数据，安装目录不写业务文件。
- 图片仅通过带认证和资格检查的 GET /image 返回 PNG data URL。SDK 前端通过 host.request 读取，前端不会收到服务器绝对路径。管理路由额外校验 admin 身份。
- Shutdown 取消轮询和外部请求，等待后台退出，关闭 HTTP 空闲连接及 SQLite。测试在 plugintest.New(t) 后注册 Shutdown，确保文件句柄先关，再由 testing 清理独立 DataDir/StateDir。
- 普通卸载保留 DataDir，显式清除数据才删除（由目标宿主负责）。备份时先停用插件，再备份整个 DataDir；同时备份宿主的插件 secret 配置。不要运行时只复制主 SQLite 文件而忽略 WAL。
- 插件 ID 为 `moonup.community`，用户入口为 `/ext/moonup.community`，管理入口为 `/admin/ext/moonup.community`。宿主按此标识创建独立安装记录和数据目录。

## 构建与检查

Go 1.27.3+、Node 与 pnpm（package.json 锁定版本）。重型检查串行，并设置：

    GOMAXPROCS=2
    GOFLAGS=-p=1
    GOMEMLIMIT=1GiB

Windows PowerShell 7.4+：./build.ps1。Linux/macOS：make test pack（竞态测试需要平台 C 编译器）。脚本构建 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64，使用新版 SDK pack 并逐包 validate。Windows 的 TMP/TEMP 临时指向 dist/tmp，避免跨磁盘原子移动问题，结束恢复环境。

单独验证：go test ./...、go vet ./...、go test -race ./...、pnpm --dir web build。纯 Go SQLite 可 CGO_ENABLED=0 构建；竞态检测另需 CGO_ENABLED=1 和可用的 C 编译器。

本地界面预览：pnpm --dir web dev。开发 mock 明确不连接真实 Telegram/模型，刷新会重置；不进入生产包。生产用户端和管理端由宿主 Web SDK 挂载。

测试覆盖 DataDir 拒绝回退、重开后读取、两个测试宿主目录隔离、未授权访问、资格与正金额充值、邀请复用/过期/入群撤销/不确定结果、并发领取、AI 主动触发/限流/预算/上下文清理、PNG/获取失败/冷却/编辑原消息，以及关闭中断后台 HTTP 并释放数据库文件。

测试宿主不模拟真实安装、更新、卸载、清除、备份恢复，也不保证真实机器人权限、外网交付或模型兼容性；上线前需在目标分发宿主用专用测试群验收。

## 字体许可

状态图内置 Community Chart Sans，基于 Noto Sans CJK 的简体中文字形子集生成，保留中日韩统一表意文字、常用标点及拉丁字符，字体名称已变更。原字体来自 https://github.com/notofonts/noto-cjk ，采用 SIL Open Font License 1.1；许可文本位于 `internal/app/fonts/OFL.txt`，并随字体一同内置二进制。
