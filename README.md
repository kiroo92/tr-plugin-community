# TokenRouter 社群中心

源码仓库：https://github.com/kiroo92/tr-plugin-community（main）。

根据 ../telegram-prototype 实现的独立插件。包含网站邀请领取、主动触发 AI 群聊、公开渠道 PNG 状态播报。没有 TG 账号绑定、套餐发放、频道订阅检查、入群验证、自动禁言或踢人。

## 兼容与安装

- 目标是 kiroo92/TokenRouter-plugin-data 分发的 TokenRouter v0.7.3 及以上；其它分发的相同版本号不代表支持 DataDir。
- Go import 保留 github.com/TokenFlux/TokenRouter/plugin-sdk/go，go.mod require v0.2.1，通过版本化 replace 选择 github.com/kiroo92/TokenRouter-plugin-data/plugin-sdk/go v0.3.0。使用 go list -m -json github.com/TokenFlux/TokenRouter/plugin-sdk/go 核对 Replace；go.sum 一同保留。
- Web SDK 固定 0.2.0，不因 DataDir 升级。打包工具统一为 go tool trplugin，不使用另一个带旧版本号的 go run。
- 从 release 中选择宿主平台对应的 tar.gz，通过宿主本地安装入口安装。此项目不自动部署、不上传官方市场、不包含分发签名索引。
- 接受 user.read、user.identities、payment.read、catalog.read 权限。

## 设置与运行

1. 创建专用 Telegram 机器人，加入无公开用户名的私有超级群组，授予管理员及邀请用户权限。不要与其它轮询程序共用；已有 Webhook 时检查会报错，插件不会替你删除。
2. 在宿主插件原生配置填写 bot_token、chat_id（-100 开头）和可选 ai_key。密钥由宿主 secret 存储，不进入 SQLite、前端或运行事件。
3. 社群管理的邀请页点击检查连接，确认后开启邀请。AI 页填写兼容 Chat Completions 的 HTTPS /v1 基础地址、模型、人设与限制，再开启助手。仅 localhost/127.0.0.1/::1 允许 HTTP。
4. 渠道页开启播报，配置 30/60/120 分钟或仅手动。消息需完整匹配查询词，AI 别名按消息开头匹配；@机器人、回复此机器人可分别启停。机器人管理员可收到群消息，但仅主动触发的正文和该成员的有限上下文被送给模型。
5. 机器人每 5 秒轮询一次，每 5 分钟复核连接及权限。只处理指定群组，新普通消息超过 2 分钟不补发；入群事件仍处理。关闭邀请只停止领取，不停止处理已发邀请。卸载/停用进程才停止后台工作。

所有功能默认关闭。公共设置保存在插件数据库；密钥和目标群组数字 ID 在宿主原生配置。切换机器人 ID 或群 ID 会进入独立业务命名空间；设置机器人权限和群规则仍由管理员负责。

## 邀请规则与边界

- 当前账号须 active，且绑定 LinuxDO 或有正金额、COMPLETED、已有支付时间的 balance 充值订单。金额使用精确有理数比较，套餐订单不计入；订单最多检查 5000 条，超出明确报错。
- 邀请使用 createChatInviteLink(member_limit=1, expire_date=10 分钟)，无需 TG 绑定或申请审批。有效链接重复领取时复用；过期未用可重新生成；匹配 chat_member 的入群事件后持久化已使用并撤销，失败会重试。
- 链接不限定身份，谁持有谁可以使用。Telegram 的人数限制不是严格累计使用次数，事件处理存在延迟；不能承诺绝对一次性、绝对本人或撤销前无竞态。
- 创建前持久化待确认状态。创建请求超时或保存结果失败时，不立即重复创建，等待该 10 分钟窗口结束；已拿到链接却保存失败会尝试撤销。若链接在崩溃窗口内被使用但未保存，或机器人离线超过 Telegram 的事件保留期，可能无法把使用事件关联到网站账号。不要将此作为严格身份访问控制。
- 每个网站账号、每组机器人/群组作用域记录一次成功使用，不保存入群者 TG 账号绑定。

## AI 与渠道

- 每位群成员独立历史，最多 30 轮，每条问题最多 1000 字符；历史请求正文限制约 24 KB。回复以纯文本发送，最多保留 4000 字符。
- 每人每分钟限流；群每日 tokens 预算按 UTC+8 重置。调用前按输入字节数、消息开销和最大回复预留，成功且返回 usage.total_tokens 时用实际值校正。失败、超时或无 usage 保留预留额；这是保护预算的保守估计，不是账单结算器。模型供应方可能采用不同计费规则。
- 调用前推进 Telegram offset，避免同一更新在进程重启后重复调用；发送中崩溃可能漏回复。无法承诺 Telegram 与模型之间的恰好一次交付。
- 渠道数据仅通过 SDK Host.Catalog.Models 读取宿主公开目录，不使用管理员抓取凭据，不泄露私有渠道。取前 12 个公开分组的窗口可用率、近期天桶和最后探测时间。缺失数据灰色展示，获取失败保留最后成功图片和时间。
- PNG 在后端生成，编号与品牌在图内，中文分组名在消息附文及网页表格对应；不依赖系统中文字体。无图片或其它文件上传功能。
- 首次 sendPhoto，之后 editMessageMedia 更新同一条消息。查询冷却限制群内刷新；发送失败保留尝试时间，避免每 5 秒反复发送。原消息被删除或失去编辑权限时会报错，不自动重复发新消息。
- 模型对话正文以明文保存在插件数据库，管理员可清理当前群的上下文；运行事件只保留最近 20 条摘要。不要在群里发送密钥或隐私信息。

## 数据、备份与生命周期

- 必须使用宿主提供的非空绝对 DataDir，Init 检查目录、写入和数据库打开；不可用即失败，不回退到当前目录或 StateDir。
- 唯一业务文件为 filepath.Join(env.DataDir, "plugin.sqlite")，SQLite 使用 WAL（运行中可能有 -wal/-shm）。邀请、使用状态、offset、历史、预算、图片及设置都持久化；StateDir 不存业务数据，安装目录不写业务文件。
- 图片仅通过带认证和资格检查的 GET /image 返回 PNG data URL。SDK 前端通过 host.request 读取，前端不会收到服务器绝对路径。管理路由额外校验 admin 身份。
- Shutdown 取消轮询和外部请求，等待后台退出，关闭 HTTP 空闲连接及 SQLite。测试在 plugintest.New(t) 后注册 Shutdown，确保文件句柄先关，再由 testing 清理独立 DataDir/StateDir。
- 普通卸载保留 DataDir，显式清除数据才删除（由目标宿主负责）。备份时先停用插件，再备份整个 DataDir；同时备份宿主的插件 secret 配置。不要运行时只复制主 SQLite 文件而忽略 WAL。
- 插件 ID 为 kiroo92.community；不会读取或迁移旧 tokenflux.telegram-access 绑定草稿的数据，也不会修改宿主数据库结构。

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
