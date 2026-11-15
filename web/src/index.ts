import { definePlugin } from '@tokenflux/tokenrouter-plugin-sdk'
import CommunityPage from './CommunityPage.vue'
import './style.css'
function register(): void { definePlugin({ pages: { home: CommunityPage, admin: CommunityPage } }) }
if (import.meta.env.DEV) void import('./dev').then(register)
else register()
