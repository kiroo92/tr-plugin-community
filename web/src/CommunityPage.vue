<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { TrButton, useHost } from '@tokenflux/tokenrouter-plugin-sdk'

// CommunityPage.vue presents website invitation claims and the community's
// administrator controls. The host route selects the user or administrator
// view, and host requests retain authentication while the component tracks
// loading, validation failures, and the permanent bot entry link. User
// copy describes Start binding and the consumed eligibility state, which
// can also require administrator review after an uncertain Telegram response.
//
// Administrator drafts cover invitations, the assistant, and channel broadcasts.
// Requests share an abort controller that cancels pending work on unmount.
// Channel previews load through authenticated
// requests and display in a dialog after rendering, keeping image retrieval
// subject to the plugin's user and administrator permission checks.

interface Settings { invites:boolean; group:string; ai:boolean; name:string; aliases:string; endpoint:string; model:string; persona:string; mention:boolean; reply:boolean; named:boolean; turns:number; rate:number; output:number; budget:number; channels:boolean; interval:number; keywords:string; cooldown:number }
// Invite describes the website owner's permanent bot entry and admission state.
interface Invite {
 user_id:number // owning website account
 url?:string // permanent bot entry
 created:number // creation Unix seconds
 used:boolean // admission claim consumed
 revoked:boolean // temporary invitation revoked
 revoke:boolean // group cleanup pending
 pending:boolean // invitation creation uncertain
 source:string // verified eligibility source
 telegram_id:number // bound Telegram account
 bound_at:number // binding Unix seconds
}
interface Group { id:number; name:string; availability?:{ availability_rate?:number; last_status?:string; last_checked_at?:string } }
interface Admin { settings:Settings; configured:boolean; ai_key_set:boolean; bot:{id:number; username:string}; group:{public:boolean; username:string; approval:boolean}; usage:{day:string; tokens:number; calls:number}; channel:{updated:number; sent:number; error:string; groups:Group[]|null}; events:{at:string; title:string; detail:string}[]|null }
interface Status { eligible:boolean; source:string; linuxdo:boolean; recharge:boolean|null; enabled:boolean; public:boolean; approval:boolean; group:string; invite:Invite; ai:boolean; name:string; channels:boolean; keywords:string; now:number }
type Condition={name:string; detail:string; state:'ok'|'no'|'skip'}
const host=useHost()
const admin=computed(()=>host.route.value.pageId==='admin')
const dark=computed(()=>host.theme.value==='dark')
const tabs=[['overview','运行概览'],['invites','邀请管理'],['ai','AI 助手'],['channels','渠道状态']] as const
type Tab=typeof tabs[number][0]
const tab=ref<Tab>('overview'), data=ref<Admin>(), settings=ref<Settings>(), status=ref<Status>()
const records=ref<Invite[]>([]), next=ref(0), busy=ref(false), failure=ref(''), notice=ref(''), image=ref(''), showImage=ref(false)
const controller=new AbortController()
const pictureDialog=ref<HTMLDialogElement>()
const conditions=computed<Condition[]>(()=>{
 const current=status.value
 if(!current)return []
 const recharge:Condition['state']=current.recharge===true?'ok':current.recharge===false?'no':'skip'
 return [
  {name:'绑定 LinuxDO',detail:current.linuxdo?'当前账号已关联 LinuxDO 身份。':'在网站个人资料中关联 LinuxDO 身份即可满足。',state:current.linuxdo?'ok':'no'},
  {name:'完成余额充值',detail:recharge==='ok'?'已有一笔成功完成的余额充值订单。':recharge==='no'?'至少一笔正金额、已付款且已完成的余额充值。':'已由 LinuxDO 绑定满足，无需核验订单。',state:recharge},
 ]
})
const satisfied=computed(()=>conditions.value.filter(item=>item.state==='ok').length)
const progress=computed(()=>satisfied.value+' / '+conditions.value.length)
// inviteState labels admission and binding status for administrator records.
// A consumed claim takes precedence; an unused entry shows the Telegram identity's
// binding state because the website bot link has no expiration time.
const inviteState=(record:Invite)=>record.used?'资格已使用':record.telegram_id?'已绑定 Telegram':'等待 Start 绑定'
function stateText(state:Condition['state']):string {return state==='ok'?'已满足':state==='skip'?'无需核验':'未满足'}
function date(value:number|string):string {return value?new Date(typeof value==='number'?value*1000:value).toLocaleString('zh-CN',{hour12:false}):'尚无记录'}
function errorText(error:unknown):string {return error instanceof Error?error.message:'请求失败，请稍后重试'}
// refresh reads the page appropriate to the current host route.
// Administrator responses replace the editable settings draft; user responses
// replace qualification, permanent entry, and Telegram binding information together.
async function refresh():Promise<void>{
 if(admin.value){data.value=await host.request<Admin>('GET','/admin/status',{signal:controller.signal});settings.value={...data.value.settings}}
 else{status.value=await host.request<Status>('GET','/status',{signal:controller.signal})}
}
async function run(action:()=>Promise<void>):Promise<void>{if(busy.value)return;busy.value=true;failure.value='';notice.value='';try{await action()}catch(error){if(!controller.signal.aborted)failure.value=errorText(error)}finally{busy.value=false}}
async function reload():Promise<void>{await run(refresh)}
async function loadRecords(after=0):Promise<void>{const result=await host.request<{records:Invite[];next:number}>('GET','/admin/invites',{query:{after:String(after)},signal:controller.signal});records.value=after?[...records.value,...result.records]:result.records;next.value=result.next}
async function selectTab(value:Tab):Promise<void>{tab.value=value;if(value==='invites')await run(()=>loadRecords())}
async function keyboard(event:KeyboardEvent,index:number):Promise<void>{let target=index;if(event.key==='ArrowRight')target=(index+1)%tabs.length;else if(event.key==='ArrowLeft')target=(index+tabs.length-1)%tabs.length;else if(event.key==='Home')target=0;else if(event.key==='End')target=tabs.length-1;else return;event.preventDefault();const value=tabs[target]![0];await selectTab(value);await nextTick();document.getElementById('community-tab-'+value)?.focus()}
async function save():Promise<void>{await run(async()=>{await host.request('POST','/admin/settings',{body:settings.value,signal:controller.signal});await refresh();notice.value='配置已保存并生效'})}
async function action(path:string,message:string):Promise<void>{await run(async()=>{await host.request('POST',path,{signal:controller.signal});await refresh();notice.value=message;if(path==='/admin/refresh')await loadImage()})}
async function loadImage():Promise<void>{const result=await host.request<{image:string}>('GET','/image',{signal:controller.signal});if(!result.image.startsWith('data:image/png;base64,'))throw new Error('图片格式无效');image.value=result.image}
async function preview():Promise<void>{await run(async()=>{await loadImage();showImage.value=true;await nextTick();pictureDialog.value?.showModal()})}
// claim requests the account's permanent bot link after website qualification.
// Repeated claims reuse the entry, and the returned status includes any Telegram
// binding already established by a private Start message.
async function claim():Promise<void>{await run(async()=>{status.value=await host.request<Status>('POST','/invite',{signal:controller.signal});notice.value='专属机器人链接已生成，长期有效。打开后点击 Start 完成绑定。'})}
// revoke invalidates the selected account's permanent bot entry after confirmation.
// The existing Telegram binding remains assigned to its owner, and a future
// website claim can obtain a replacement entry for that same identity.
async function revoke(record:Invite):Promise<void>{if(!await host.confirm({title:'撤销机器人入口？',message:'当前专属链接将失效。用户可重新领取，已有 Telegram 绑定继续保留。',danger:true}))return;await run(async()=>{await host.request('POST','/admin/revoke',{body:{user_id:record.user_id},signal:controller.signal});await loadRecords();notice.value='机器人入口已撤销'})}
async function clear():Promise<void>{if(await host.confirm({title:'清理 AI 上下文？',message:'删除当前群组保存的全部对话正文，无法恢复。',danger:true}))await action('/admin/clear','已清理对话上下文')}
// copy places the displayed bot entry in the browser clipboard.
// Browsers that deny clipboard access leave the readonly URL available for manual
// copying and show an error beside the page controls.
async function copy():Promise<void>{try{await navigator.clipboard.writeText(status.value?.invite.url||'');notice.value='机器人专属链接已复制'}catch{
 // Clipboard access depends on browser permission and secure-context rules. Keep
 // the displayed URL selectable so users can complete copying through browser controls.
 failure.value='无法自动复制，请选中链接复制'
}}
onMounted(()=>{void reload()})
watch(admin,()=>{image.value='';showImage.value=false;void reload()})
// Cancel unfinished host requests when this page is destroyed so their callbacks
// cannot publish stale qualification or binding information into an inactive view.
onBeforeUnmount(()=>{controller.abort()})
</script>

<template>
<div class="community" :class="{'cm-dark':dark}">
 <header class="cm-heading"><div><p class="cm-eyebrow">{{ admin?'COMMUNITY / 社群管理':'COMMUNITY / 社群中心' }}</p><h1>{{ admin?'社群管理':'社群中心' }}</h1><p>{{ admin?'邀请、AI 助手与渠道播报，一处管理。':'领取专属机器人链接，绑定账号后加入交流群。' }}</p></div><TrButton :loading="busy" @click="reload">刷新</TrButton></header>
 <p v-if="failure" class="cm-alert cm-error" role="alert">{{ failure }}</p><p v-if="notice" class="cm-alert" role="status">{{ notice }}</p>
 <template v-if="!admin && status">
  <div class="cm-grid">
   <section class="cm-card cm-entry">
    <div class="cm-entry-main">
     <div class="cm-group"><span class="cm-logo" aria-hidden="true">↗</span><div><p class="cm-group-label">Telegram 群组</p><h2>加入 {{ status.group }}</h2></div></div>
     <p>与同路人交流模型、接入方法和使用心得。</p>
     <ul class="cm-facts"><li>专属机器人入口</li><li>长期有效</li><li>Start 自动绑定</li><li>绑定账号自动审批</li></ul>
    </div>
    <div class="cm-invite-area">
     <div class="cm-invite-head"><h2>领取你的机器人专属链接</h2><span class="cm-pill" :class="!status.enabled?'cm-warn':status.invite.telegram_id?'cm-ok':status.eligible?'cm-brand':''">{{ !status.enabled?'暂未开放':status.invite.telegram_id?'已绑定':status.eligible?'可领取':'未达标' }}</span></div>
     <p v-if="!status.enabled">管理员尚未开放社群入口，请稍后再来。</p>
     <template v-else-if="status.invite.url">
      <p>此链接长期有效。打开机器人并点击 Start，将自动绑定你的 Telegram 账号，再收到群邀请。</p>
      <p v-if="status.invite.telegram_id">已绑定 Telegram：{{ status.invite.telegram_id }}。入群申请将按此账号自动核验。</p>
      <input class="cm-link" readonly :value="status.invite.url" aria-label="机器人专属链接">
      <div class="cm-actions"><a class="cm-primary" :href="status.invite.url" target="_blank" rel="noopener noreferrer">打开机器人绑定 ↗</a><TrButton @click="copy">复制链接</TrButton></div>
      <p v-if="status.invite.used">入群资格已使用或正在核实。如尚未入群，请联系管理员。</p>
     </template>
     <template v-else>
      <p>生成你的长期机器人入口。首次 Start 自动绑定账号，机器人随后私发入群邀请。</p>
      <div class="cm-actions"><button class="cm-primary" :disabled="busy||!status.enabled||!status.eligible" @click="claim">{{ busy?'处理中…':'领取机器人专属链接' }} <span aria-hidden="true">↗</span></button></div>
     </template>
    </div>
   </section>
   <aside class="cm-card cm-criteria" aria-label="入群资格">
    <div class="cm-criteria-inner">
     <div class="cm-criteria-head"><div><p class="cm-eyebrow">入群资格</p><h2>满足任一条件即可</h2></div><span class="cm-progress">{{ progress }}</span></div>
     <div class="cm-verdict" :class="{'cm-no':!status.eligible}" role="status"><span class="cm-verdict-icon" aria-hidden="true">{{ status.eligible?'✓':'!' }}</span><div><b>{{ status.eligible?'已满足，可领取邀请':'暂未满足' }}</b><p>{{ status.eligible?'领取专属机器人链接，点击 Start 自动完成 Telegram 绑定。':'下方完成任意一项后即可领取。' }}</p></div></div>
     <div class="cm-conditions">
      <template v-for="(item,index) in conditions" :key="item.name">
       <div v-if="index" class="cm-divider">或者</div>
       <div class="cm-condition" :class="{'cm-ok':item.state==='ok'}"><span class="cm-condition-icon" aria-hidden="true">{{ item.name.startsWith('绑定')?'L':'¥' }}</span><div class="cm-condition-body"><div class="cm-condition-title"><span>{{ item.name }}</span><span class="cm-pill" :class="item.state==='ok'?'cm-ok':''">{{ stateText(item.state) }}</span></div><p>{{ item.detail }}</p></div></div>
      </template>
     </div>
     <p class="cm-criteria-foot">领取和 Start 时核验资格，首次 Start 自动绑定 Telegram 账号。</p>
    </div>
   </aside>
  </div>
  <div class="cm-fine"><div class="cm-fine-item"><span aria-hidden="true">⚠</span><p><b>专属链接请勿转发。</b>首次 Start 会绑定当前 Telegram 账号，之后仅该账号可以使用；每个网站账号绑定一人。</p></div><div class="cm-fine-item"><span aria-hidden="true">ⓘ</span><p><b>账号与账单问题走工单。</b>群内请勿发布广告、敏感信息或完整 API 密钥，个人问题请通过支持工单联系管理员。</p></div></div>
  <section class="cm-card"><h2>入群之后</h2><div class="cm-tools-grid"><div class="cm-tool"><span class="cm-tool-icon" aria-hidden="true">✦</span><div><h3>{{ status.ai?'和'+status.name+'聊聊你的问题':'AI 群聊助手暂未开放' }}</h3><p>@机器人、回复它的消息，或按管理员设置呼叫别名，就能问使用方式与模型经验。</p></div></div><div class="cm-tool"><span class="cm-tool-icon" aria-hidden="true">▥</span><div><h3>查看渠道状态</h3><p>{{ status.channels?'发送「'+status.keywords.split(/[,，]/)[0]+'」，查看公开渠道的探测结果、可用率和更新时间。':'渠道状态播报暂未开放。' }}</p><TrButton @click="preview">查看最近状态图</TrButton></div></div></div></section>
 </template>
 <template v-if="admin && data && settings">
  <nav class="cm-tabs" role="tablist" aria-label="社群管理"><button v-for="([value,label],index) in tabs" :id="'community-tab-'+value" :key="value" role="tab" :aria-selected="tab===value" :aria-controls="'community-panel-'+value" :tabindex="tab===value?0:-1" @click="selectTab(value)" @keydown="keyboard($event,index)">{{ label }}</button></nav>
  <section v-show="tab==='overview'" id="community-panel-overview" role="tabpanel" aria-labelledby="community-tab-overview">
   <div class="cm-alert" :class="{'cm-ok-alert':data.configured}">{{ data.configured?'机器人凭据已配置，请先检查连接与权限。':'请先在宿主「插件配置」填写 Bot Token、群组 ID 和模型 API Key。' }}</div>
   <div class="cm-stats"><article class="cm-card"><p>机器人</p><strong>{{ data.bot.username?'@'+data.bot.username:'待检查' }}</strong><small>{{ data.group.public?('公开群'+(data.group.username?' @'+data.group.username:'')+(data.group.approval?', 审批已开（绑定账号通过）':', 未开审批，无门禁')):'私有群' }}</small></article><article class="cm-card"><p>今日 AI 回复</p><strong>{{ data.usage.calls }} <small>次</small></strong><small>{{ data.usage.day }} · UTC+8</small></article><article class="cm-card"><p>今日模型用量</p><strong>{{ data.usage.tokens.toLocaleString() }}</strong><small>含失败调用预留 · 预算 {{ settings.budget.toLocaleString() }}</small></article><article class="cm-card"><p>渠道图更新</p><strong>{{ data.channel.updated?new Date(data.channel.updated*1000).toLocaleTimeString():'尚未更新' }}</strong><small>{{ settings.interval?'每 '+settings.interval+' 分钟':'仅手动' }}</small></article></div>
   <div class="cm-two"><article class="cm-card"><h2>功能运行状态</h2><div v-for="item in [{name:'邀请领取',enabled:settings.invites,tab:'invites'},{name:'AI 群聊助手',enabled:settings.ai,tab:'ai'},{name:'渠道状态播报',enabled:settings.channels,tab:'channels'}]" :key="item.name" class="cm-feature"><b>{{ item.name }}</b><span class="cm-pill" :class="item.enabled?'cm-ok':''">{{ item.enabled?'已开启':'已暂停' }}</span><button class="cm-text" @click="selectTab(item.tab as Tab)">管理 →</button></div><p>群组：{{ settings.group }}</p></article><article class="cm-card"><h2>最近运行事件</h2><p v-if="!data.events?.length">尚无运行记录。</p><ol class="cm-events"><li v-for="(event,index) in data.events" :key="index"><time>{{ date(event.at) }}</time><b>{{ event.title }}</b><p>{{ event.detail }}</p></li></ol></article></div>
  </section>
  <section v-show="tab==='invites'" id="community-panel-invites" role="tabpanel" aria-labelledby="community-tab-invites" class="cm-two">
   <form class="cm-card" @submit.prevent="save"><h2>入群设置</h2><label class="cm-toggle"><span>开放入群领取</span><input v-model="settings.invites" type="checkbox"></label><label>群组名称<input v-model="settings.group" maxlength="40" required></label><div class="cm-alert">Bot Token 和 Chat ID 由宿主插件配置保存。机器人须为超级群组管理员并拥有邀请权限。用户通过专属链接点击 Start 后自动绑定，入群申请按绑定账号批准；公开群须开启群级入群审批。</div><p>LinuxDO 绑定或已完成余额充值，满足任意一项。机器人链接长期有效，每个网站账号绑定一个 Telegram 账号。</p><div class="cm-actions"><TrButton variant="primary" :loading="busy" @click="save">保存配置</TrButton><TrButton :disabled="busy||!data.configured" @click="action('/admin/check','机器人连接与邀请权限正常')">检查连接</TrButton></div></form>
   <article class="cm-card"><h2>邀请领取记录</h2><p>显示网站账号和自动绑定的 Telegram 账号。</p><div class="cm-table"><table><thead><tr><th>账号 ID</th><th>Telegram ID</th><th>资格来源</th><th>状态</th><th>领取时间</th><th>操作</th></tr></thead><tbody><tr v-for="record in records" :key="record.user_id"><td>{{ record.user_id }}</td><td>{{ record.telegram_id||'等待绑定' }}</td><td>{{ record.source }}</td><td>{{ inviteState(record) }}</td><td>{{ date(record.created) }}</td><td><button class="cm-text" :disabled="busy||record.used" @click="revoke(record)">撤销</button></td></tr><tr v-if="!records.length"><td colspan="6">暂无领取记录。</td></tr></tbody></table></div><TrButton v-if="next" :loading="busy" @click="run(()=>loadRecords(next))">加载更多</TrButton></article>
  </section>
  <section v-show="tab==='ai'" id="community-panel-ai" role="tabpanel" aria-labelledby="community-tab-ai" class="cm-two cm-ai-grid">
   <form class="cm-card" @submit.prevent="save"><h2>助手配置</h2><label class="cm-toggle"><span>启用 AI 助手</span><input v-model="settings.ai" type="checkbox"></label><div class="cm-fields"><label>助手名称<input v-model="settings.name" maxlength="24" required></label><label>呼叫别名<input v-model="settings.aliases" maxlength="60"><small>多个别名用逗号分隔，按消息开头匹配。</small></label></div><label>模型接口地址<input v-model="settings.endpoint" type="url" required><small>远程接口须 HTTPS，填写 /v1 基础地址。</small></label><label>模型名称<input v-model="settings.model" maxlength="100" :required="settings.ai"></label><p class="cm-alert">API Key：{{ data.ai_key_set?'已由宿主加密保存':'请在宿主插件配置中填写' }}。密钥不会返回本页。</p><label>助手人设<textarea v-model="settings.persona" rows="5" maxlength="2000" required></textarea></label><fieldset><legend>群聊触发方式</legend><label><input v-model="settings.mention" type="checkbox"> @机器人</label><label><input v-model="settings.reply" type="checkbox"> 回复机器人</label><label><input v-model="settings.named" type="checkbox"> 呼叫别名</label></fieldset><div class="cm-fields"><label>上下文轮数<input v-model.number="settings.turns" type="number" min="1" max="30" required></label><label>每人每分钟消息数<input v-model.number="settings.rate" type="number" min="1" max="30" required></label><label>单次回复上限 · tokens<input v-model.number="settings.output" type="number" min="128" max="4096" required></label><label>群级每日预算 · tokens<input v-model.number="settings.budget" type="number" min="1000" max="10000000" required></label></div><div class="cm-actions"><TrButton variant="primary" :loading="busy" @click="save">保存配置</TrButton><TrButton :disabled="busy" @click="clear">清理上下文</TrButton></div></form>
   <article class="cm-card cm-chat"><div class="cm-chat-head"><span class="cm-symbol" aria-hidden="true">✦</span><div><h2>{{ settings.name }}</h2><p>群聊使用示例</p></div><span class="cm-pill" :class="settings.ai?'cm-ok':''">{{ settings.ai?'已开启':'已暂停' }}</span></div><div class="cm-bubbles"><p class="cm-bubble cm-me">@机器人 如何开始使用 API？</p><p class="cm-bubble">先在网站创建 API 密钥，再根据接入文档配置客户端。<br><small>此为排版示例，真实回复由你配置的模型生成。</small></p></div><p>只有主动触发才调用模型。每个群成员单独保存有限上下文；不把整群聊天自动送给模型。</p><p>失败或返回不含 usage 的调用保留预留预算。上下文以明文保存在插件数据库，可随时清理；普通日志不记录正文。</p></article>
  </section>
  <section v-show="tab==='channels'" id="community-panel-channels" role="tabpanel" aria-labelledby="community-tab-channels" class="cm-two cm-channel-grid">
   <form class="cm-card" @submit.prevent="save"><h2>播报设置</h2><label class="cm-toggle"><span>启用渠道状态播报</span><input v-model="settings.channels" type="checkbox"></label><label>发送到群组<input :value="settings.group" readonly></label><label>定时更新频率<select v-model.number="settings.interval"><option :value="30">每 30 分钟</option><option :value="60">每 60 分钟</option><option :value="120">每 2 小时</option><option :value="0">仅手动更新</option></select></label><label>手动查询关键词<input v-model="settings.keywords" maxlength="100" required><small>用逗号分隔；群消息需完整匹配。</small></label><label>查询冷却 · 秒<input v-model.number="settings.cooldown" type="number" min="10" max="3600" required></label><div class="cm-alert">数据来自宿主公开模型目录。只显示公开分组，未知数据不视为故障。首次发送后更新同一条消息。</div><TrButton variant="primary" :loading="busy" @click="save">保存配置</TrButton></form>
   <article class="cm-card"><h2>群内状态图</h2><p v-if="data.channel.error" class="cm-error">{{ data.channel.error }}</p><div class="cm-image"><img v-if="image" :src="image" alt="公开渠道历史探测与可用率"><p v-else>点击查看最近图片，或刷新公开数据生成新图。</p></div><p>最近获取：{{ date(data.channel.updated) }} · 最近发送：{{ date(data.channel.sent) }}</p><div class="cm-actions"><TrButton :disabled="busy" @click="preview">查看最近图片</TrButton><TrButton :disabled="busy" @click="action('/admin/refresh','公开渠道数据与预览已刷新')">刷新预览</TrButton><TrButton variant="primary" :disabled="busy||!settings.channels||!data.configured" @click="action('/admin/send','群内状态图已更新')">发送到群</TrButton></div><div class="cm-table"><table><thead><tr><th>公开分组</th><th>窗口可用率</th><th>最近状态</th></tr></thead><tbody><tr v-for="group in data.channel.groups" :key="group.id"><td>#{{ group.id }} {{ group.name }}</td><td>{{ group.availability?.availability_rate!=null?(group.availability.availability_rate*100).toFixed(1)+'%':'暂无数据' }}</td><td>{{ group.availability?.last_status||'暂无数据' }}</td></tr></tbody></table></div><small>图中使用分组 ID 与品牌，名称在消息附文中对应。最多 12 个公开分组。</small></article>
  </section>
 </template>
 <p v-if="busy&&!status&&!data" role="status">正在读取社群状态…</p>
 <dialog ref="pictureDialog" v-if="showImage" class="cm-modal" aria-label="渠道状态图" @click.self="showImage=false" @close="showImage=false"><section class="cm-card"><div class="cm-criteria-head"><h2>最近公开渠道状态</h2><button class="cm-text" autofocus @click="showImage=false">关闭 ×</button></div><div class="cm-image"><img :src="image" alt="公开渠道探测图"></div></section></dialog>
</div>
</template>
