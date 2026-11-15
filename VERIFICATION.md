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

## 未验证及限制

- go test -race ./... 未完成：默认 CGO_ENABLED=0；显式启用后报 C compiler gcc not found。本机没有可用 C 编译器，未擅自安装。八组普通测试已通过，此项不能视为竞态检测通过。
- 未在目标真实宿主执行安装、升级、卸载/清除或备份恢复；DataDir 生命周期依据指定 SDK README，文件持久化与隔离通过 plugintest 独立目录验证。
- 未使用真实 Telegram 群、Bot Token 或模型密钥。外部 API 由本地 HTTP fixture 验证，真实权限、网络与模型供应方兼容性仍需专用测试群验收。
- 不提供上传功能；图片由后端生成并经过鉴权接口读取。没有修改宿主、旧绑定草稿或其他正式插件，没有部署线上服务。源码与带签名索引的安装包按用户后续要求发布到 kiroo92/tr-plugin-community。
- 邀请无法限定本人，Telegram 人数限制与事件撤销不能提供绝对一次性保证。其它交付及数据边界见 README.md。
