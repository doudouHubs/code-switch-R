import { createRouter, createWebHashHistory } from 'vue-router'
import MainPage from '../components/Main/Index.vue'
import LogsPage from '../components/Logs/Index.vue'
import GeneralPage from '../components/General/Index.vue'
import SkillPage from '../components/Skill/Index.vue'
import PromptsPage from '../components/Prompts/Index.vue'
import ConsolePage from '../components/Console/Index.vue'
import TrayPage from '../components/Tray/Index.vue'
import ProjectManagerPage from '../components/ProjectManager/Index.vue'
import ProjectManagerSessionDetailPage from '../components/ProjectManager/SessionDetail.vue'
import RadarPage from '../components/Radar/Index.vue'
import PetWindowPage from '../components/Pet/PetWindow.vue'
import PetSettingsPage from '../components/Pet/PetSettings.vue'

const routes = [
  { path: '/', component: MainPage },
  { path: '/prompts', component: PromptsPage },
  { path: '/skill', component: SkillPage },
  { path: '/logs', component: LogsPage },
  { path: '/console', component: ConsolePage },
  { path: '/projects', component: ProjectManagerPage },
  { path: '/projects/sessions/:sessionId', component: ProjectManagerSessionDetailPage },
  { path: '/radar', component: RadarPage },
  { path: '/pet', component: PetWindowPage },
  { path: '/pet/settings', component: PetSettingsPage },
  { path: '/settings', component: GeneralPage },
  { path: '/tray', component: TrayPage },
]

export default createRouter({
  history: createWebHashHistory(), // Use createWebHashHistory for hash-based routing
  routes
})
