import { configureMock, HostRequestError } from '@tokenflux/tokenrouter-plugin-sdk/dev'
const settings={"invites":true,"group":"TokenRouter 交流群","ai":true,"name":"小路","aliases":"小路","endpoint":"https://api.example.com/v1","model":"community-chat","persona":"你是 TokenRouter 社群助手，友好简洁地帮助用户了解产品。","mention":true,"reply":true,"named":true,"turns":10,"rate":6,"output":1024,"budget":100000,"channels":true,"interval":60,"keywords":"渠道状态,状态","cooldown":60}
let invite={user_id:9,url:'',expires:0,created:0,used:false,revoked:false,revoke:false,pending:false,source:'LinuxDO 绑定'}
configureMock(async(method,path,options)=>{
 const now=Math.floor(Date.now()/1000)
 if(path==='/image'){const canvas=document.createElement('canvas');canvas.width=900;canvas.height=300;const context=canvas.getContext('2d')!;context.fillStyle='#0f172a';context.fillRect(0,0,900,300);context.fillStyle='#06b6d4';context.font='24px sans-serif';context.fillText('TOKENROUTER / CHANNEL STATUS',36,55);context.fillStyle='#e2e8f0';context.font='18px sans-serif';context.fillText('DEVELOPMENT PREVIEW - NO LIVE DATA',36,105);context.fillText('#7 Claude',36,170);for(let index=0;index<14;index++){context.fillStyle=index===8?'#eab308':'#22c595';context.fillRect(300+index*22,145,14,32)}context.fillStyle='#94a3b8';context.fillText('Sample only / not connected to Telegram',36,250);return {image:canvas.toDataURL('image/png')}}
 if(path==='/admin/status')return {settings,configured:false,ai_key_set:false,bot:{id:0,username:''},usage:{day:new Date().toISOString().slice(0,10),tokens:24800,calls:86},channel:{updated:0,sent:0,error:'本地界面预览，不会连接真实机器人或模型。',groups:[]},events:[{at:new Date().toISOString(),title:'本地开发预览',detail:'数据为示例，正式版由宿主接口提供。'}]}
 if(path==='/status'||path==='/invite'){if(method==='POST')invite={...invite,url:'https://t.me/+LOCAL_PREVIEW_ONLY',expires:now+600,created:now};return {eligible:true,source:'LinuxDO 绑定',linuxdo:true,recharge:null,enabled:true,group:settings.group,invite,ai:true,name:settings.name,channels:true,keywords:settings.keywords,now}}
 if(path==='/admin/settings'){Object.assign(settings,options?.body);return {saved:true}}
 if(path==='/admin/invites')return {records:invite.created?[invite]:[],next:0}
 throw new HostRequestError('本地预览不执行真实社群操作',501,'PREVIEW_ONLY')
})
