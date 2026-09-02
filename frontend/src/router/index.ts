import { createRouter, createWebHashHistory } from 'vue-router'
import MainPage from '../components/Main/Index.vue'
import LogsPage from '../components/Logs/Index.vue'
import GeneralPage from '../components/General/Index.vue'
import SkillPage from '../components/Skill/Index.vue'
import PromptsPage from '../components/Prompts/Index.vue'
import ConsolePage from '../components/Console/Index.vue'
import ProjectManagerPage from '../components/ProjectManager/Index.vue'
import ProjectManagerSessionDetailPage from '../components/ProjectManager/SessionDetail.vue'
import RadarPage from '../components/Radar/Index.vue'
import AgentManagerPage from '../components/Agent/Index.vue'
import PetWindowPage from '../components/Pet/PetWindow.vue'
import PetSettingsPage from '../components/Pet/PetSettings.vue'
import ChannelsPage from '../components/Channels/Index.vue'

const routes = [
  { path: '/', component: MainPage },
  { path: '/prompts', component: PromptsPage },
  { path: '/skill', component: SkillPage },
  { path: '/logs', component: LogsPage },
  { path: '/console', component: ConsolePage },
  { path: '/projects', component: ProjectManagerPage },
  { path: '/projects/sessions/:sessionId', component: ProjectManagerSessionDetailPage },
  { path: '/channels', component: ChannelsPage },
  { path: '/radar', component: RadarPage },
  // Agent 是唯一需要跨页面保留的长会话入口；其它页面离开后应卸载，释放轮询和事件订阅。
  { path: '/agent', component: AgentManagerPage, meta: { keepAlive: true } },
  { path: '/pet', component: PetWindowPage },
  { path: '/pet/settings', component: PetSettingsPage },
  // 旧入口仅做兼容跳转，实际页面归属于宠物设置的“心跳”页签。
  { path: '/pet/heartbeat', redirect: { path: '/pet/settings', query: { tab: 'heartbeat' } } },
  { path: '/settings', component: GeneralPage },
]

export default createRouter({
  history: createWebHashHistory(), // Use createWebHashHistory for hash-based routing
  routes
})
