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

## v0.2.5 发行核验（2026-11-15）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.5。标签指向 `63eff8802d4539dfdb17d6ba92b05c5b2a88148d`，已公开为 latest，插件标识继续使用 moonup.community。
- 18 组测试、vet、竞态检测及前端类型检查通过。五平台纯 Go 构建、打包与 SDK validate 通过，包内源码版本均匹配标签，前端包含 Start 自动绑定文案并移除领取倒计时。
- 使用现有签名密钥，在 moonup.community 索引条目新增 0.2.5 并保留 0.2.4。独立签名验证与篡改拒绝通过，11 个草稿资产下载后大小、SHA256、manifest 摘要及索引条目均与本地一致。
- 公开后匿名下载 latest 的索引、签名、公钥及五平台安装包，验签和全部安装包摘要核对通过。仓库未配置 Actions 工作流，以上验证在本地执行。
- 网站领取、私聊 Start 绑定、私发群邀请、缺少邀请来源时按身份审批已由本地夹具串联验证。部署后仍需真实 Telegram 验收；此次发布未更新线上宿主。

## 群邀请取消有效期（v0.2.6，2026-11-15）

- 群邀请创建省略 expire_date，记录的 expires 固定为 0；Telegram 返回了到期时间的结果会被拒绝。机器人入口和群邀请均不设时间限制。
- 回归测试验证一年后及进程重开仍复用同一群邀请，旧版带到期时间的链接先撤销再重建，生成事件不再包含有效期或分钟提示，永久链接撤销失败保留待重试状态。
- 群邀请创建结果不明时使用独立的 API 重试时点，不把重试等待作为链接有效期。重复 Start 不会因经过一段时间而重新签发已知邀请。
- 19 组 Go 测试、go vet 和竞态检查通过。真实 Telegram 验收需更新后再次 Start，以获取替换旧版限时链接的永久邀请。

## v0.2.6 发行核验（2026-11-15）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.6。标签指向 `826930d2b9ba3adcf4ece58b64ca85713dc612a8`，已公开为 latest。
- 19 组 Go 测试、vet、竞态检测、前端类型检查与构建通过，五平台纯 Go 编译、打包及 SDK validate 通过。
- 现有签名密钥生成的索引新增 0.2.6 并保留历史版本。签名验证及篡改拒绝通过，11 个草稿资产下载后与本地文件逐一核对大小和 SHA256，manifest 摘要与五个平台条目匹配。
- 公开后匿名下载 latest 索引、签名、公钥与五平台安装包，验签和全部包摘要核对通过。仓库未配置 Actions 工作流，验证在本地完成。
- 更新后再次 Start 会替换旧版限时群邀请。此次发布未更新线上宿主，真实 Telegram 链接属性仍待部署后验收。

## 机器人命令（v0.2.7，2026-11-16）

- 群里回复用户后发送 `/id`，本地 Telegram 夹具验证返回被回复用户的数字 ID 和当前群 ID，话题及回复位置正确。覆盖大整数 ID、机器人用户名大小写、匿名管理员、频道身份及缺失发送者。
- 验证命令先于 AI 触发处理，发给其他机器人的命令、过旧群消息、其他群和机器人消息被跳过，普通提及仍调用 AI。邀请和 AI 关闭时基础命令可用。
- 私聊 `/status` 重新核验本人资格，`/invite` 重复使用同一永久邀请；未绑定用户无法拿到他人邀请。群状态隐藏网站账号及专属码，回复他人发送 `/reset` 只删除自己的上下文。
- 命令菜单验证私聊和目标群两个作用域、第二次写入失败后的完整重试、进程重开后的同步复用。菜单错误不使权限检查失效，Telegram API 冷却仍对后续调用生效。
- 24 组 Go 测试、`go vet` 和 CGO 竞态检查通过。测试使用本地 API 夹具，真实 Telegram 的菜单展示和回复查 ID 待更新后验收。

## v0.2.7 发行核验（2026-11-16）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.7。标签指向 `d8723e0462cca0920963ff8495390828c36b7180`，已公开为 latest。
- 24 组 Go 测试、vet、竞态检测、前端类型检查和构建通过，五平台纯 Go 编译、打包及 SDK validate 通过。安装包内源码版本与标签一致，VCS 状态为 clean。
- 索引沿用现有签名公钥，新增 0.2.7 并保留历史版本；11 个草稿资产下载后逐一核对大小和 SHA256，索引验签、manifest 摘要与篡改拒绝检查通过。
- 公开后匿名下载 latest 索引、签名、公钥及五平台安装包，全部 11 个资产与本地一致。仓库没有 Actions 工作流，源代码推送后的 check runs 和 status 检查数均为 0；构建和测试在本地完成。
- 真实 Telegram 命令菜单及回复查 ID 需更新后验收，本次发布未更新线上宿主。

## 渠道图与独立绑定查询（v0.2.8，2026-11-16）

- 状态图按参考采用深绿标题栏、浅色列表、中文分组、历史色块、可用率、首字 P50、缓存命中；不把账号数量画进图内。使用当前 moonup.dev 公开目录生成完整预览并检查排版；内置字体含中文、比较符号及 OFL 许可，图片不依赖系统字体。
- 群附文只在最后列出 `[GPT满血] 可用账号：...` 与 `[GPT特惠] 可用账号：...`。测试覆盖按分组 ID 对应、测得零、未知分组、无效数量、重复分组、旧快照及读取失败清空旧数量；未使用真实群发送测试消息。
- `/binding` 使用 `X-Plugin-Admin-Key` 独立鉴权；成功正文严格为是/否。测试覆盖未配置密钥、错误密钥、重复头、无效/重复 ID、已绑定、未绑定、入口撤销、资格已使用、跨机器人/群组隔离与存储错误。
- 28 组 Go 测试、vet、CGO 竞态检查通过。真实 Telegram 发送、宿主公开路由及透明号池联调需安装后验收。

## v0.2.8 发行核验（2026-11-16）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.8。标签指向 `5b084af6fddd97babbff7c281cc5dc47a4543609`，已公开为 latest。
- 28 组 Go 测试、vet、竞态检测、前端类型检查与生产构建通过。五平台纯 Go 构建、打包及 SDK validate 通过；安装包中的源码 revision 与标签一致，构建时 VCS 为 clean。
- 11 个草稿资产下载后与本地逐一核对大小和 SHA256，索引签名、manifest 摘要及篡改拒绝通过。公开后匿名读取 latest 索引、签名、公钥与五平台安装包，全部资产再次核对一致。
- 本次只发布社群中心。透明号池源码取自已有私有仓库 `kiroo92/tr-plugin-remote-account-pool` 的 v0.1.2 主线；配套统计代码已推送并打包，但按用户要求尚未发行 v0.1.3，latest 仍为 v0.1.2。号池统计接口未启用时，社群附文显示暂无数据。
- 仓库没有 Actions 工作流，推送后的 check runs 为 0；上述构建和检查在本地执行。未更新线上宿主，真实公开绑定查询路由、Telegram 状态图发送及可选号池统计待安装后验收。

## 状态图替换与网站 ID 查询（v0.2.9，2026-11-16）

- 排查线上统计接口时，已安装透明号池 v0.1.3 返回 404「公开号池统计未启用」。用户开启原生统计开关后，公开接口返回 HTTP 200、stale=false；14:29 UTC+8 快照的 GPT 满血可用数为 548，特惠为 183。此验证只读取统计接口，未向真实群发送测试消息。
- `/id` 通过被回复消息的 Telegram 发送者反查绑定的网站账号，返回网站用户 ID；直接发送查询本人，匿名和未绑定状态给出说明。帮助及菜单同步更新，保留话题内回复、机器人用户名后缀及基础命令开关独立性。
- 播报每次发送新图，保存新图 ID 和清理队列后删除旧图。测试验证发送失败保留旧图、发送与删除顺序、删除失败后的跨重启重试、播报暂停时继续清理、已不存在的旧图完成清理、权限及服务故障保留队列，并跳过当前图 ID。
- 32 组 Go 测试、vet 和竞态检查通过。真实 Telegram 的新图发送与旧图删除、网站 ID 查询需更新后验收；本地夹具已验证对应处理。

## v0.2.9 发行核验（2026-11-16）

- 发布地址：https://github.com/kiroo92/tr-plugin-community/releases/tag/v0.2.9。标签指向 `3ebcfb86f85418a49626cde23c85f64787534424`，已公开为 latest。
- 32 组 Go 测试、vet、竞态检测、前端类型检查与构建、五平台纯 Go 编译及 SDK validate 通过，包内源码 revision 与标签一致，构建时 VCS 状态 clean。
- 11 个草稿资产下载后逐一核对大小和 SHA256，索引签名、manifest 摘要与篡改拒绝通过。公开后匿名下载 latest 索引、签名、公钥及全部五平台包，再次核对全部 11 个资产一致。
- 统计读取已用线上公开接口确认恢复；新消息发送及旧图删除、网站用户 ID 回复使用本地 Telegram 夹具验证，未向真实群发送测试消息，也未更新线上社群插件。
- 源码推送后的 check runs 为 0，仓库未配置 Actions 工作流，构建及验证在本地完成。

## 未验证及限制

- 早期 Windows 验证因缺少 gcc 未完成竞态检查；本次 Linux 本地修改已启用 CGO 并通过完整竞态检查，见上节。
- 未在目标真实宿主执行安装、升级、卸载/清除或备份恢复；DataDir 生命周期依据指定 SDK README，文件持久化与隔离通过 plugintest 独立目录验证。
- 未使用真实 Telegram 群、Bot Token 或模型密钥。外部 API 由本地 HTTP fixture 验证，真实权限、网络与模型供应方兼容性仍需专用测试群验收。
- 不提供上传功能；图片由后端生成并经过鉴权接口读取。没有修改宿主、旧绑定草稿或其他正式插件，没有部署线上服务。源码与带签名索引的安装包按用户后续要求发布到 kiroo92/tr-plugin-community。
- 永久入口在首次 Start 前被转发时，持有人可能先完成绑定。绑定后仅固定 Telegram 账号能使用此资格，最多由插件自动批准一次；管理员或其它机器人批准的结果由群组管理者负责。其它交付及数据边界见 README.md。
