# 验证记录（2026-11-15）

- Go SDK：require github.com/TokenFlux/TokenRouter/plugin-sdk/go v0.2.1；Replace = github.com/kiroo92/TokenRouter-plugin-data/plugin-sdk/go v0.3.0。go list -m -json 与构建产物 go version -m 均确认；Go SDK 模块校验值 h1:LzMxi9BmuuDPnRXIjaS4aagc6wcy+odFBC7vrsB6U+M=。
- Web SDK：0.2.0，保持不变。go.mod、go.sum、pnpm-lock.yaml 已纳入源码。
- 重型命令均按 GOMAXPROCS=2、GOFLAGS=-p=1、GOMEMLIMIT=1GiB 串行执行。
- go test ./...：9 组测试通过；覆盖持久化重开、隔离、邀请及权限、创建结果不确定、AI 限流预算、PNG/冷却/失败、并发领取和 Shutdown 取消请求/关闭数据库；新增 /status 分项资格（LinuxDO 满足时跳过订单核验、两项均未满足、仅充值满足）测试。
- go vet ./...：通过。
- pnpm --dir web build：Vue/TypeScript 类型检查及生产构建通过。
- 浏览器开发壳：用户领取、配置保存、错误反馈、键盘标签切换、原生图片弹窗/Escape；桌面和 360/390 手机、深色检查通过，无页面控制台错误、无整页横向溢出。verification/*.jpg 为开发 mock 截图，不是真实业务记录。
- 靛紫配色改造（同日）：用户页改为「群组+领取」合并卡与独立资格卡；资格卡按后端 linuxdo/recharge 分项显示已满足、未满足、无需核验，并给出 n/2 进度与结论条。四个管理页保留原有结构与能力。插件作用域内把 --tr-color-brand/--tr-color-accent 指向靛紫，宿主按钮、复选框和焦点环随之统一；深色下主色提亮、按钮文字转深色。开发壳实测刷新按钮浅色 rgb(79,70,229)/白字、深色 rgb(165,180,252)/深字，390/360 无横向溢出。verification/ui-*.jpg 为改造后截图。
- ./build.ps1：五个平台纯 Go 编译和 go tool trplugin pack/validate 全部通过。release/checksums.txt 的五个 SHA256 已逐项核对。检查 linux/amd64 包只包含 manifest、README、图标、前端资源和可执行文件。
- 打包脚本使用 go tool trplugin，没有写死旧 SDK 版本的命令；本项目没有额外发布工作流。
- 自建分发 v0.1.0：重新执行 build.ps1 通过，以完整草稿上传 11 个资产后下载核对五平台包 SHA256/大小及 manifest 哈希；SDK index sign 校验索引结构并签名，独立 Ed25519 验签及篡改拒绝通过。公开为 latest 后匿名下载固定来源 index.json 和 index.json.sig，再次核对原始字节并验签通过。来源和公钥见 README；私钥只保存在用户配置目录，未进入 Git 或 Release。宿主内添加来源与实际安装仍未验证。
- 自建分发 v0.2.0：manifest 升至 0.2.0；build.ps1 串行执行 go test/go vet、frozen install、前端构建及五平台编译打包，五包逐个 trplugin validate 通过（旧 0.1.0 包先移出 release 目录，避免与新版 checksums 混检）。索引保留 0.1.0 条目并新增 0.2.0，使用同一私钥 index sign。认证下载草稿 11 个资产逐一与本地 SHA256/大小一致，manifest 哈希匹配，签名有效且篡改被拒；公开为 latest 后匿名下载 index.json、index.json.sig 与 linux/amd64 包，验签通过且包哈希与索引一致。包内前端确认为靛紫变量（#4f46e5 / #a5b4fc），plugin.json 为 0.2.0。

- 公开群与入群审批（2026-11-15 追加）：依据 Bot API `getChat.join_by_request` 定义——
  「True, if all users directly joining the supergroup without using an invite link need to be approved」，
  以及 `createChatInviteLink` 只要求机器人是管理员。据此移除「必须无公开用户名」的硬限制，改为只要求 `supergroup`。
  检查连接时读取 `username`、`join_by_request` 存入作用域，`/admin/status` 与 `/status` 返回 `group`/`public`/`approval`，
  管理概览与用户页显示当前模式。新增测试 TestPublicGroupAndJoinRequests：公开群 + 审批开启可通过检查并正常领取；
  转发链接产生的 `chat_join_request` 触发一次 `declineChatJoinRequest` 且不把邀请标记为已使用；普通群仍被拒绝（502）。
  十组 Go 测试、go vet、前端类型检查与构建通过；开发壳确认用户页显示「本链接直接入群，无需审批」、管理端显示
  「公开群 @moonup_chat, 入群审批已开」。未在真实公开群验证审批与链接并存的行为。

- 自建分发 v0.2.1：build.ps1 串行执行 go test（10 组）、go vet、frozen install、前端构建（含公开群文案）与五平台编译打包，五包逐个 validate 通过；索引保留 0.2.0、0.1.0 并新增 0.2.1，使用同一私钥签名。草稿 11 资产认证下载后与本地 SHA256/大小逐一一致，manifest 哈希匹配，验签通过且篡改被拒；公开为 latest 后匿名读取 latest 来源，版本为 0.2.1、验签通过，linux/amd64 与 windows/amd64 包哈希与索引一致。包内确认含「公开或私有」新文案与 0.2.1 版本号。

- 入群审批按链接判定（v0.2.2，2026-11-15）：用户实测公开群开启群级审批后邀请链接同样需要审批，推翻此前「链接直接进群」的假设。改为订阅 chat_join_request，用 ChatInviteLink.invite_link 反查记录：匹配且未使用/未过期/未撤销则 approveChatJoinRequest，其余 declineChatJoinRequest；批准前先落库已使用，失败回滚。TestPublicGroupAndJoinRequests 覆盖「插件链接被批准并标记已使用」与「非插件链接被拒绝」，10 组 Go 测试、go vet、前端类型检查与构建通过。注意：未在真实公开群端到端验证审批回调；机器人缺 can_invite_users 时无法批准或拒绝。

- 自建分发 v0.2.2：build.ps1 通过（10 组测试、vet、前端构建、五平台打包与逐个 validate），索引保留 0.2.1/0.2.0/0.1.0 并新增 0.2.2 签名；草稿 11 资产认证下载与本地逐一一致，验签通过、篡改被拒、manifest 哈希匹配；公开为 latest 后匿名读取 latest 来源，版本 0.2.2、验签通过，linux/amd64 包哈希一致；包内二进制确认含 approveChatJoinRequest 路径。

## 单链接单人审批（v0.2.3，2026-11-15）

- 创建参数改为 `creates_join_request=true`，省略 `member_limit`。邀请记录新增 `join_request` 标记，旧普通链接在领取页隐藏，重新领取时先撤销再替换。
- 本地 HTTP 夹具验证同一批次两个请求仅批准一人、撤销失败后仍拒绝第二人、重启后保持已用名额、明确 400 拒绝后可重试，以及批准成功但连接中断时保持锁定。另覆盖旧链接撤销失败与替换、缺少链接和未匹配记录的分类事件。
- `GOMAXPROCS=2 GOFLAGS=-p=1 GOMEMLIMIT=1GiB CGO_ENABLED=1 make test` 通过：13 组测试、`go vet ./...` 和 `go test -race ./...`。首次竞态检查发现丢失响应夹具的控制字段缺少同步，加入处理完成通知后完整重跑通过。
- `pnpm --dir web install --frozen-lockfile` 与 `pnpm --dir web build` 通过，后者完成 Vue/TypeScript 检查和生产资源构建。改动的 Go 文件已格式化，`git diff --check` 通过。
- 已同步使用说明、管理规则、变更记录和页面文案。此节记录实现验证；发行状态见 GitHub Release 与发行核验记录。真实 Telegram 公开群与私有群的审批回调及最终入群结果待验收。

## v0.2.3 发行核验（2026-11-15）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.3。标签指向源码提交 `7178d1bfba338438fffd6d9f8c5b85b5e40794ae`，已公开为 latest。
- `make pack` 完成 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64 的纯 Go 编译、打包及 SDK validate。各包内 manifest 均为 0.2.3，二进制构建信息指向该源码提交，工作区标记为未修改。
- 沿用已验签的历史索引，保留 0.2.2、0.2.1、0.2.0、0.1.0 并新增 0.2.3。按维护者要求轮换签名密钥，新公钥见 README；私钥位于本地发布配置目录，并保存为仓库 Actions secret `INDEX_SIGNING_KEY`。
- SDK index sign 通过结构校验并签名；独立 Ed25519 验签通过，改动索引字节后签名被拒。草稿全部 11 个资产下载后，大小及 SHA256 与本地文件逐一一致，manifest 摘要和五个安装包条目匹配。
- 公开后匿名下载 latest 的索引、签名及公钥，均与发布文件一致并通过验签。五个平台安装包全部匿名下载，大小和 SHA256 与签名索引一致。仓库未配置 GitHub Actions 工作流，验证在本地完成。
- 已有来源需替换公钥后再刷新更新，升级后重新领取审批邀请。真实 Telegram 入群结果与目标宿主安装流程仍待验收。

## v0.2.4 发行核验（2026-11-15）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.4。标签指向源码提交 `b9e9878e50ddf4586008063a2fc22dbd9ec2e5db`，已公开为 latest。
- 插件标识与前端构建统一为 `moonup.community`，用户入口 `/ext/moonup.community`，管理入口 `/admin/ext/moonup.community`。产物 CSS 作用域匹配新标识；按用户要求不提供旧地址跳转或数据迁移。
- `GOMAXPROCS=2 GOFLAGS=-p=1 GOMEMLIMIT=1GiB CGO_ENABLED=1 make test` 通过：13 组 Go 测试、vet 和竞态检测。`make pack` 完成前端类型检查与构建、五平台编译、打包及 SDK validate，各二进制均指向标签源码且构建时工作区干净。
- 使用 v0.2.3 的同一签名密钥，新索引新增 `moonup.community` 0.2.4 条目；旧标识保留历史版本并标记下架。SDK 签名、独立 Ed25519 验签及篡改拒绝通过，11 个草稿资产全部下载核对大小和 SHA256。
- 正式公开后匿名下载 latest 索引、签名、公钥与五平台安装包，验签与全部包摘要核对通过。仓库未配置 GitHub Actions 工作流，验证在本地完成。
- 新标识需独立安装并填写配置，安装前停用旧实例以免共用机器人轮询。此次发行未在目标宿主执行安装。

## 永久机器人入口与 Start 绑定（v0.2.5，2026-11-15）

- 网站 `/invite` 返回 `https://t.me/<bot>?start=<专属随机码>`，网页响应的 expires 为 0。链接经过一年虚拟时间及进程重开后保持不变，领取时不创建群邀请，页面已去除倒计时。
- 私聊 Start 持久化网站账号与 Telegram ID 的一一绑定后，再创建需审批的群邀请并私发。测试通过网站 HTTP 入口、序列化私聊 Update 和公开群缺少 invite_link 的入群 Update 验证完整链路；未绑定申请被拒绝，已绑定申请获准。
- 覆盖转发专属链接后拒绝改绑、同一 Telegram 账号拒绝绑定第二个网站账号、绑定和群邀请重启后复用、私聊发送失败重放、明确拒绝投递后继续处理其他用户、重新 Start 更新临时群邀请，以及无效载荷、群内 Start、资格失效、撤销和暂停。
- 管理列表和网站响应隐藏原始 token 字段；本人网站机器人 URL 保留所需 payload。群邀请仅在机器人私聊投递，管理列表隐藏其 URL。
- 本地全部 18 组 Go 测试、go vet 和竞态检查通过，前端 Vue/TypeScript 检查及生产构建通过。测试使用本地 Telegram 夹具，真实机器人私聊和入群结果待部署后验收。

## 未验证及限制

- 早期 Windows 验证因缺少 gcc 未完成竞态检查；本次 Linux 本地修改已启用 CGO 并通过完整竞态检查，见上节。
- 未在目标真实宿主执行安装、升级、卸载/清除或备份恢复；DataDir 生命周期依据指定 SDK README，文件持久化与隔离通过 plugintest 独立目录验证。
- 未使用真实 Telegram 群、Bot Token 或模型密钥。外部 API 由本地 HTTP fixture 验证，真实权限、网络与模型供应方兼容性仍需专用测试群验收。
- 不提供上传功能；图片由后端生成并经过鉴权接口读取。没有修改宿主、旧绑定草稿或其他正式插件，没有部署线上服务。源码与带签名索引的安装包按用户后续要求发布到 kiroo92/tr-plugin-community。
- 永久入口在首次 Start 前被转发时，持有人可能先完成绑定。绑定后仅固定 Telegram 账号能使用此资格，最多由插件自动批准一次；管理员或其它机器人批准的结果由群组管理者负责。其它交付及数据边界见 README.md。
