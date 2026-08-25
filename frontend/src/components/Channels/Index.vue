<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Events } from '../../wails-runtime-compat'
import {
  Archive,
  ArrowLeft,
  ChevronDown,
  CircleAlert,
  Eye,
  EyeOff,
  FolderOpen,
  Info,
  Play,
  RefreshCw,
  Search,
  Shield,
  Square,
  Trash2,
  X,
} from '@lucide/vue'
import ChannelIcon from './ChannelIcon.vue'
import { createWeixinQrDataUrl } from './weixinQr'
import {
  listChannelDescriptors,
  listChannelInstances,
  listChannelProjects,
  removeChannelInstance,
  saveChannelInstance,
  setChannelEnabled,
  startChannel,
  stopChannel,
  startWeixinLogin,
  waitWeixinLogin,
  cancelWeixinLogin,
  type ChannelDescriptor,
  type ChannelInstance,
  type ProjectBinding,
} from '../../services/channels'
import { showToast } from '../../utils/toast'

const { t } = useI18n()
const router = useRouter()

const PLATFORM_CATEGORIES = [
  { key: 'china', labelKey: 'channels.categories.china', types: ['feishu-bot', 'dingtalk-bot', 'wecom-bot', 'qq-bot', 'weixin-official'] },
  { key: 'international', labelKey: 'channels.categories.international', types: ['telegram-bot', 'discord-bot', 'whatsapp-bot'] },
] as const
const PLATFORM_TYPES: ReadonlySet<string> = new Set(PLATFORM_CATEGORIES.flatMap((category) => category.types))

const instances = ref<ChannelInstance[]>([])
const projects = ref<ProjectBinding[]>([])
const descriptors = ref<ChannelDescriptor[]>([])
const draft = ref<ChannelInstance | null>(null)
const selectedId = ref('')
const searchQuery = ref('')
const loading = ref(true)
const refreshing = ref(false)
const savingId = ref('')
const errorMessage = ref('')
const showArchived = ref(false)
const secretVisibility = ref<Record<string, boolean>>({})
const readablePathDraft = ref('')
const weixinLoginPending = ref(false)
const weixinQrUrl = ref('')
const weixinQrSource = ref('')
const weixinLoginStatus = ref('')
const weixinLoginMessage = ref('')

let saveTimer: ReturnType<typeof setTimeout> | null = null
let stopEvent: (() => void) | null = null
let weixinLoginRun = 0
let weixinSessionInstanceID = ''
let weixinSessionKey = ''

const descriptorByType = computed(() => new Map(descriptors.value.map((descriptor) => [descriptor.type, descriptor])))
const selectedDescriptor = computed(() => descriptorByType.value.get(draft.value?.type ?? ''))
const selectedProject = computed(() => projects.value.find((project) => project.id === draft.value?.projectId))
const activeStatus = computed(() => draft.value?.status || 'stopped')

// 后端已经以 canonical/archived 作为事实源；这里再按 id 去重，避免旧 bridge 或热刷新快照
// 在同一帧返回重复记录时把重复项直接渲染到左侧列表。
const uniqueInstances = computed(() => {
  const byID = new Map<string, ChannelInstance>()
  for (const instance of instances.value) {
    if (!instance.id) continue
    const previous = byID.get(instance.id)
    if (!previous || instance.updatedAt >= previous.updatedAt) byID.set(instance.id, instance)
  }
  return [...byID.values()]
})

const activeInstances = computed(() => {
  const seenBuiltinTypes = new Set<string>()
  return uniqueInstances.value.filter((instance) => {
    if (instance.archived) return false
    if (!instance.builtin) return true
    // canonical 迁移完成后每种内置平台只应有一条 active 记录；旧进程返回重复快照时，
    // 左栏仍保持“一个平台一个入口”，避免用户误以为存在多个可独立配置的同类频道。
    if (seenBuiltinTypes.has(instance.type)) return false
    seenBuiltinTypes.add(instance.type)
    return true
  })
})
const archivedInstances = computed(() => uniqueInstances.value.filter((instance) => instance.archived))

const filteredActiveInstances = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  if (!query) return activeInstances.value
  return activeInstances.value.filter((instance) => {
    const descriptor = descriptorByType.value.get(instance.type)
    return [instance.name, instance.type, descriptor?.displayName, descriptor?.description]
      .some((value) => String(value ?? '').toLowerCase().includes(query))
  })
})

const customInstances = computed(() => filteredActiveInstances.value.filter((instance) => {
  return !PLATFORM_TYPES.has(instance.type)
}))

function cloneInstance(instance: ChannelInstance): ChannelInstance {
  return {
    ...instance,
    config: { ...(instance.config ?? {}) },
    tools: { ...(instance.tools ?? {}) },
    features: {
      autoReply: instance.features?.autoReply ?? true,
      streamingReply: instance.features?.streamingReply ?? true,
      autoStart: instance.features?.autoStart ?? true,
    },
    permissions: {
      allowReadHome: instance.permissions?.allowReadHome ?? false,
      readablePathPrefixes: [...(instance.permissions?.readablePathPrefixes ?? [])],
      allowWriteOutside: instance.permissions?.allowWriteOutside ?? false,
      allowShell: instance.permissions?.allowShell ?? false,
      allowSubAgents: instance.permissions?.allowSubAgents ?? false,
    },
    projectId: instance.projectId ?? null,
    providerPlatform: instance.providerPlatform ?? '',
    providerId: instance.providerId ?? null,
    model: instance.model ?? null,
  }
}

function descriptorFor(instance: ChannelInstance) {
  return descriptorByType.value.get(instance.type)
}

function isCustom(instance: ChannelInstance) {
  return !instance.builtin && !instance.archived
}

function displayName(instance: ChannelInstance) {
  return instance.builtin ? (descriptorFor(instance)?.displayName || instance.name) : instance.name
}

function projectName(instance: ChannelInstance) {
  if (!instance.projectId) return t('channels.unbound')
  return projects.value.find((project) => project.id === instance.projectId)?.name || instance.projectId
}

function statusClass(status: string) {
  return `status-${status || 'stopped'}`
}

function statusLabel(status: string) {
  switch (status) {
    case 'running': return t('channels.status.running', 'Running')
    case 'error': return t('channels.status.error', 'Error')
    case 'starting': return t('channels.status.starting', 'Starting')
    default: return t('channels.status.stopped', 'Stopped')
  }
}

function categoryInstances(types: readonly string[]) {
  // 后端快照的返回顺序不属于 UI 契约；按原版 descriptor 顺序展开，避免平台列表随数据库顺序漂移。
  return types.flatMap((type) => filteredActiveInstances.value.filter((instance) => instance.type === type))
}

function goToProjects() {
  void router.push('/projects')
}

function matchesInstance(instance: ChannelInstance) {
  return selectedId.value === instance.id
}

function normalizeNullable(value: string | null | undefined) {
  const trimmed = String(value ?? '').trim()
  return trimmed ? trimmed : null
}

function payloadFromDraft(instance: ChannelInstance): ChannelInstance {
  const payload = cloneInstance(instance)
  payload.name = payload.name.trim() || descriptorFor(payload)?.displayName || payload.type
  payload.projectId = normalizeNullable(payload.projectId)
  // 旧版频道可能带有 provider/model 字段，但频道运行时已经统一继承客户端默认
  // Codex 配置；保存时清理覆盖项，避免历史快照继续诱导后续代码走旧路由。
  payload.providerPlatform = ''
  payload.providerId = null
  payload.model = null
  payload.config = Object.fromEntries(Object.entries(payload.config).map(([key, value]) => [key, String(value ?? '')]))
  return payload
}

async function persistDraft(instance: ChannelInstance | null, showSuccess = false): Promise<boolean> {
  if (!instance || instance.archived) return true
  const payload = payloadFromDraft(instance)
  savingId.value = payload.id
  try {
    await saveChannelInstance(payload)
    const current = instances.value.find((item) => item.id === payload.id)
    if (current) {
      Object.assign(current, cloneInstance({ ...current, ...payload, status: current.status || payload.status }))
    }
    if (draft.value?.id === payload.id) {
      draft.value = cloneInstance({ ...draft.value, ...payload, status: draft.value.status || payload.status })
    }
    if (showSuccess) showToast(t('channels.toast.saved'))
    return true
  } catch (error) {
    showToast(error instanceof Error ? error.message : String(error), 'error')
    return false
  } finally {
    if (savingId.value === payload.id) savingId.value = ''
  }
}

function scheduleSave() {
  if (!draft.value || draft.value.archived) return
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => {
    saveTimer = null
    void persistDraft(draft.value)
  }, 500)
}

async function flushPendingSave() {
  if (!saveTimer) return true
  clearTimeout(saveTimer)
  saveTimer = null
  return persistDraft(draft.value)
}

async function selectInstance(instance: ChannelInstance) {
  // 切换平台前先冲刷当前字段的防抖保存，避免用户刚改完凭据就切换导致内容丢失。
  await cancelActiveWeixinLogin()
  await flushPendingSave()
  selectedId.value = instance.id
  draft.value = cloneInstance(instance)
  secretVisibility.value = {}
  readablePathDraft.value = ''
  weixinQrUrl.value = ''
  weixinQrSource.value = ''
  weixinLoginStatus.value = ''
  weixinLoginMessage.value = ''
  if (instance.archived) showArchived.value = true
}

async function load() {
  loading.value = true
  errorMessage.value = ''
  try {
    const [nextInstances, nextProjects, nextDescriptors] = await Promise.all([
      listChannelInstances(),
      listChannelProjects(),
      listChannelDescriptors(),
    ])
    instances.value = nextInstances.map(cloneInstance)
    projects.value = nextProjects
    descriptors.value = nextDescriptors

    // 初始选中必须复用左侧列表的去重结果；否则旧进程返回多条同类型内置实例时，
    // 详情可能打开一个已经被列表隐藏的副本，造成“列表和详情对不上”的错觉。
    const visibleInstances = [...activeInstances.value, ...archivedInstances.value]
    const selected = visibleInstances.find((instance) => instance.id === selectedId.value)
      || visibleInstances[0]
    if (!selected) {
      draft.value = null
      return
    }

    selectedId.value = selected.id
    draft.value = cloneInstance(selected)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    loading.value = false
  }
}

async function refresh() {
  refreshing.value = true
  try {
    await flushPendingSave()
    await load()
  } finally {
    refreshing.value = false
  }
}

async function toggleEnabled(nextEnabled: boolean) {
  if (!draft.value || draft.value.archived) return
  const previous = draft.value.enabled
  if (nextEnabled && !draft.value.projectId) {
    draft.value.enabled = false
    showToast(t('channels.validation.projectRequired'), 'error')
    return
  }
  draft.value.enabled = nextEnabled
  try {
    await setChannelEnabled(draft.value.id, nextEnabled)
    const current = instances.value.find((instance) => instance.id === draft.value?.id)
    if (current) current.enabled = nextEnabled
    showToast(nextEnabled ? t('channels.toast.enabled') : t('channels.toast.disabled'))
  } catch (error) {
    draft.value.enabled = previous
    showToast(error instanceof Error ? error.message : String(error), 'error')
  }
}

async function toggleInstanceEnabled(instance: ChannelInstance, nextEnabled: boolean) {
  if (instance.archived) return
  if (draft.value?.id !== instance.id) await selectInstance(instance)
  await toggleEnabled(nextEnabled)
}

function handleProjectChange() {
  if (!draft.value || draft.value.archived) return
  // 解绑项目时同步关闭频道，满足后端“启用频道必须绑定项目”的约束，
  // 让自动保存不会把一个无法启动的 enabled=true 快照写进数据库。
  if (!draft.value.projectId) draft.value.enabled = false
  scheduleSave()
}

async function start() {
  if (!draft.value || draft.value.archived) return
  if (!draft.value.projectId) {
    showToast(t('channels.validation.projectRequired'), 'error')
    return
  }
  await flushPendingSave()
  try {
    if (!draft.value.enabled) {
      draft.value.enabled = true
      await setChannelEnabled(draft.value.id, true)
    }
    await startChannel(draft.value.id)
    draft.value.status = 'running'
    const current = instances.value.find((instance) => instance.id === draft.value?.id)
    if (current) {
      current.enabled = true
      current.status = 'running'
    }
    showToast(t('channels.toast.started'))
  } catch (error) {
    showToast(error instanceof Error ? error.message : String(error), 'error')
    await refresh()
  }
}

async function stop() {
  if (!draft.value || draft.value.archived) return
  try {
    await stopChannel(draft.value.id)
    draft.value.status = 'stopped'
    const current = instances.value.find((instance) => instance.id === draft.value?.id)
    if (current) current.status = 'stopped'
    showToast(t('channels.toast.stopped'))
  } catch (error) {
    showToast(error instanceof Error ? error.message : String(error), 'error')
  }
}

async function remove() {
  if (!draft.value || draft.value.builtin || draft.value.archived) return
  const name = displayName(draft.value)
  if (!window.confirm(t('channels.removeConfirm', { name }))) return
  await flushPendingSave()
  const removedID = draft.value.id
  try {
    await removeChannelInstance(removedID)
    instances.value = instances.value.filter((instance) => instance.id !== removedID)
    const next = activeInstances.value[0] || archivedInstances.value[0]
    if (next) {
      await selectInstance(next)
    } else {
      draft.value = null
    }
    showToast(t('channels.toast.removed'))
  } catch (error) {
    showToast(error instanceof Error ? error.message : String(error), 'error')
  }
}

function toggleSecret(key: string) {
  secretVisibility.value[key] = !secretVisibility.value[key]
}

function isWeixinInstance(instance: ChannelInstance | null | undefined) {
  return instance?.type === 'weixin-official'
}

function platformHasCredentials(instance: ChannelInstance) {
  if (isWeixinInstance(instance)) return Boolean(instance.config?.token?.trim())
  return Object.entries(instance.config ?? {}).some(([key, value]) => {
    return key !== 'systemPrompt' && Boolean(String(value ?? '').trim())
  })
}

function weixinQrCandidate(value: { qrDataUrl?: string; qrUrl?: string; qrcode?: string }) {
  const candidate = String(value.qrDataUrl || value.qrUrl || value.qrcode || '').trim()
  return candidate.startsWith('data:image/') || candidate.startsWith('https://') || candidate.startsWith('http://')
    ? candidate
    : ''
}

async function updateWeixinQr(
  value: { qrDataUrl?: string; qrUrl?: string; qrcode?: string },
  isCurrent?: () => boolean,
) {
  const source = weixinQrCandidate(value)
  if (!source || source === weixinQrSource.value) return Boolean(weixinQrUrl.value)
  const image = await createWeixinQrDataUrl(source)
  if (isCurrent && !isCurrent()) return false
  weixinQrSource.value = source
  weixinQrUrl.value = image
  return true
}

function weixinStatusClass(status: string) {
  return `weixin-status-${status || 'idle'}`
}

async function cancelActiveWeixinLogin() {
  weixinLoginRun += 1
  const instanceID = weixinSessionInstanceID
  const sessionKey = weixinSessionKey
  weixinSessionInstanceID = ''
  weixinSessionKey = ''
  weixinLoginPending.value = false
  if (!instanceID || !sessionKey) return
  try {
    await cancelWeixinLogin(instanceID, sessionKey)
  } catch {
    // 后端在确认成功、过期或重新开始时也会清理 session；取消失败不应覆盖当前页面状态。
  }
}

async function bindWeixin() {
  const instance = draft.value
  if (!instance || instance.archived || !isWeixinInstance(instance)) return

  const instanceID = instance.id
  await cancelActiveWeixinLogin()
  // cancelActiveWeixinLogin 会递增 generation，避免旧 Wait 请求在重新绑定后把二维码
  // 或凭据写回当前页面；本次流程沿用递增后的 generation。
  const currentRun = weixinLoginRun

  if (!await flushPendingSave()) {
    weixinLoginStatus.value = 'error'
    weixinLoginMessage.value = t('channels.weixin.saveBeforeLoginFailed')
    return
  }
  if (currentRun !== weixinLoginRun) return

  weixinLoginPending.value = true
  weixinLoginStatus.value = 'starting'
  weixinLoginMessage.value = t('channels.weixin.loginStarting')
  weixinQrUrl.value = ''
  weixinQrSource.value = ''
  weixinSessionInstanceID = instanceID

  try {
    const started = await startWeixinLogin(instanceID)
    if (currentRun !== weixinLoginRun) {
      if (started.sessionKey) {
        try {
          await cancelWeixinLogin(instanceID, started.sessionKey)
        } catch {
          // 页面已切换时只做尽力清理，不把旧流程错误显示到新频道。
        }
      }
      return
    }
    if (!started.sessionKey) throw new Error(t('channels.weixin.qrCodeFailed'))

    weixinSessionKey = started.sessionKey
    await updateWeixinQr(started, () => currentRun === weixinLoginRun)
    if (currentRun !== weixinLoginRun) return
    weixinLoginStatus.value = started.status || 'wait'
    weixinLoginMessage.value = started.message || t('channels.weixin.scanHint')
    if (!weixinQrUrl.value) throw new Error(t('channels.weixin.qrCodeFailed'))

    // 后端一次 Wait 只轮询一个短窗口；前端持续续轮询，才能同时支持扫码中、确认中、
    // 二维码过期自动刷新和重新绑定，而不会把长时间阻塞塞进 Wails bridge。
    while (currentRun === weixinLoginRun) {
      const waited = await waitWeixinLogin(instanceID, weixinSessionKey)
      if (currentRun !== weixinLoginRun) return
      const refreshedQr = weixinQrCandidate(waited)
      if (refreshedQr) await updateWeixinQr(waited, () => currentRun === weixinLoginRun)
      if (currentRun !== weixinLoginRun) return
      weixinLoginStatus.value = waited.status || 'wait'
      weixinLoginMessage.value = waited.message || t('channels.weixin.scanHint')

      if (waited.connected && waited.token) {
        const nextConfig = {
          ...instance.config,
          baseUrl: waited.baseUrl || instance.config.baseUrl || 'https://ilinkai.weixin.qq.com',
          token: waited.token,
          accountId: waited.accountId || instance.config.accountId || '',
          userId: waited.userId || instance.config.userId || '',
        }
        // 登录成功前不碰旧 token；成功后才一次性提交新凭据，避免扫码失败把可用账号清空。
        const nextInstance = cloneInstance(instance)
        nextInstance.config = nextConfig
        nextInstance.enabled = Boolean(nextInstance.projectId)
        if (!await persistDraft(nextInstance)) throw new Error(t('channels.weixin.saveFailed'))

        if (nextInstance.projectId) {
          await setChannelEnabled(nextInstance.id, true)
          await startChannel(nextInstance.id)
          if (draft.value?.id === nextInstance.id) draft.value.status = 'running'
          const current = instances.value.find((item) => item.id === nextInstance.id)
          if (current) current.status = 'running'
          showToast(t('channels.weixin.loginSuccess'))
        } else {
          if (draft.value?.id === nextInstance.id) draft.value.status = 'stopped'
          showToast(t('channels.weixin.loginSavedBindProject'))
        }
        weixinLoginStatus.value = 'confirmed'
        weixinQrUrl.value = ''
        weixinQrSource.value = ''
        return
      }

      if (waited.status === 'expired' && !refreshedQr) {
        throw new Error(waited.message || t('channels.weixin.qrExpired'))
      }
    }
  } catch (error) {
    if (currentRun !== weixinLoginRun) return
    weixinLoginStatus.value = 'error'
    weixinLoginMessage.value = error instanceof Error ? error.message : String(error)
    showToast(weixinLoginMessage.value, 'error')
  } finally {
    if (currentRun === weixinLoginRun) {
      const sessionKey = weixinSessionKey
      weixinSessionInstanceID = ''
      weixinSessionKey = ''
      weixinLoginPending.value = false
      if (sessionKey) {
        try {
          await cancelWeixinLogin(instanceID, sessionKey)
        } catch {
          // 登录成功时后端已经清理；这里仅兜底清理失败流程的 session。
        }
      }
    }
  }
}

function setFeature(key: keyof ChannelInstance['features'], value: boolean) {
  if (!draft.value || draft.value.archived) return
  draft.value.features[key] = value
  scheduleSave()
}

function setPermission(key: keyof ChannelInstance['permissions'], value: boolean) {
  if (!draft.value || draft.value.archived) return
  if (typeof draft.value.permissions[key] !== 'boolean') return
  draft.value.permissions[key] = value as never
  scheduleSave()
}

function setTool(tool: string, value: boolean) {
  if (!draft.value || draft.value.archived) return
  draft.value.tools[tool] = value
  scheduleSave()
}

function addReadablePath() {
  if (!draft.value || draft.value.archived) return
  const path = readablePathDraft.value.trim()
  if (!path || draft.value.permissions.readablePathPrefixes.includes(path)) {
    readablePathDraft.value = ''
    return
  }
  draft.value.permissions.readablePathPrefixes.push(path)
  readablePathDraft.value = ''
  scheduleSave()
}

function removeReadablePath(path: string) {
  if (!draft.value || draft.value.archived) return
  draft.value.permissions.readablePathPrefixes = draft.value.permissions.readablePathPrefixes.filter((item) => item !== path)
  scheduleSave()
}

function eventPayload(event: unknown): Record<string, unknown> | null {
  if (!event || typeof event !== 'object') return null
  const data = (event as { data?: unknown }).data
  if (Array.isArray(data) && data.length === 1 && data[0] && typeof data[0] === 'object') return data[0] as Record<string, unknown>
  if (data && typeof data === 'object') return data as Record<string, unknown>
  return event as Record<string, unknown>
}

function onChannelEvent(event: unknown) {
  const payload = eventPayload(event)
  if (!payload) return
  const instanceId = String(payload.instanceId ?? '')
  if (!instanceId) return
  const channelData = payload.data && typeof payload.data === 'object' ? payload.data as Record<string, unknown> : payload
  const state = typeof channelData.state === 'string' ? channelData.state : ''
  const current = instances.value.find((instance) => instance.id === instanceId)
  if (current && state) current.status = state
  if (draft.value?.id === instanceId && state) draft.value.status = state
}

onMounted(async () => {
  stopEvent = Events.On('channels.event', onChannelEvent)
  await load()
})

onBeforeUnmount(() => {
  if (saveTimer) clearTimeout(saveTimer)
  stopEvent?.()
  void cancelActiveWeixinLogin()
})
</script>

<template>
  <section class="channels-page">
    <div v-if="errorMessage" class="channels-error"><CircleAlert :size="16" /> {{ errorMessage }} <button type="button" @click="refresh">{{ t('common.retry', 'Retry') }}</button></div>
    <div v-if="loading" class="channels-state"><RefreshCw :size="18" class="spinning" /> {{ t('channels.loading') }}</div>

    <div v-else class="channels-content">
      <header class="channels-context">
        <div class="channels-context-copy">
          <p class="channels-context-eyebrow">{{ t('channels.eyebrow', 'CHAT CHANNELS') }}</p>
          <h1>{{ selectedProject?.name || t('channels.title', 'Chat Channels') }}</h1>
          <p>{{ selectedProject?.path || t('channels.context.noProject', 'No project is currently bound') }}</p>
        </div>
        <div class="channels-context-actions">
          <button class="outline-action context-action" type="button" @click="goToProjects">
            <ArrowLeft :size="14" />
            {{ t('channels.actions.backProject', 'Return to projects') }}
          </button>
          <button class="outline-action context-action" type="button" :disabled="refreshing" @click="void refresh()">
            <RefreshCw :size="14" :class="{ spinning: refreshing }" />
            {{ t('channels.actions.refresh', 'Refresh') }}
          </button>
        </div>
      </header>

      <div class="channels-workspace">
        <aside class="platforms-panel">
        <div class="platforms-header">
          <p class="platforms-title">{{ t('channels.platforms', 'Platforms') }}</p>
          <div class="platform-search">
            <Search :size="14" />
            <input v-model="searchQuery" :placeholder="t('channels.search', 'Search channels...')" />
          </div>
        </div>

        <div class="platforms-scroll">
          <section v-for="category in PLATFORM_CATEGORIES" :key="category.key" v-show="categoryInstances(category.types).length" class="platform-category">
            <p class="category-label">{{ t(category.labelKey, category.key === 'china' ? 'China' : 'International') }}</p>
            <div class="platform-items">
              <div
                v-for="instance in categoryInstances(category.types)"
                :key="instance.id"
                class="platform-item"
                :class="{ selected: matchesInstance(instance), disabled: !instance.enabled }"
                role="button"
                tabindex="0"
                @click="void selectInstance(instance)"
                @keydown.enter="void selectInstance(instance)"
                @keydown.space.prevent="void selectInstance(instance)"
              >
                <ChannelIcon :icon="descriptorFor(instance)?.icon || ''" :size="22" :muted="!instance.enabled" />
                <div class="platform-copy">
                  <div class="platform-name-row"><span class="platform-name">{{ displayName(instance) }}</span><span v-if="instance.projectId" class="bound-mark"><FolderOpen :size="10" />{{ t('channels.bound') }}</span><span v-else-if="instance.type === 'weixin-official' && platformHasCredentials(instance)" class="bound-mark account-bound">{{ t('channels.weixin.accountBoundShort') }}</span><span class="status-dot" :class="statusClass(instance.status)" /></div>
                  <p>{{ descriptorFor(instance)?.description || instance.type }}</p>
                </div>
                <button
                  class="channel-switch"
                  :class="{ on: instance.enabled }"
                  type="button"
                  role="switch"
                  :aria-checked="instance.enabled"
                  :aria-label="instance.enabled ? t('channels.actions.disable', 'Disable') : t('channels.actions.enable', 'Enable')"
                  :disabled="instance.archived"
                  @click.stop="void toggleInstanceEnabled(instance, !instance.enabled)"
                ><span /></button>
              </div>
            </div>
          </section>

          <section v-if="customInstances.length" class="platform-category">
            <p class="category-label">{{ t('channels.categories.custom', 'Custom') }}</p>
            <div class="platform-items">
              <div
                v-for="instance in customInstances"
                :key="instance.id"
                class="platform-item"
                :class="{ selected: matchesInstance(instance), disabled: !instance.enabled }"
                role="button"
                tabindex="0"
                @click="void selectInstance(instance)"
                @keydown.enter="void selectInstance(instance)"
                @keydown.space.prevent="void selectInstance(instance)"
              >
                <ChannelIcon :icon="descriptorFor(instance)?.icon || ''" :size="22" :muted="!instance.enabled" />
                <div class="platform-copy">
                  <div class="platform-name-row"><span class="platform-name">{{ displayName(instance) }}</span><span v-if="instance.projectId" class="bound-mark"><FolderOpen :size="10" />{{ t('channels.bound') }}</span><span v-else-if="instance.type === 'weixin-official' && platformHasCredentials(instance)" class="bound-mark account-bound">{{ t('channels.weixin.accountBoundShort') }}</span><span class="status-dot" :class="statusClass(instance.status)" /></div>
                  <p>{{ descriptorFor(instance)?.description || instance.type }}</p>
                </div>
                <button
                  class="channel-switch"
                  :class="{ on: instance.enabled }"
                  type="button"
                  role="switch"
                  :aria-checked="instance.enabled"
                  :aria-label="instance.enabled ? t('channels.actions.disable', 'Disable') : t('channels.actions.enable', 'Enable')"
                  @click.stop="void toggleInstanceEnabled(instance, !instance.enabled)"
                ><span /></button>
              </div>
            </div>
          </section>

          <section v-if="archivedInstances.length" class="archived-category">
            <button class="archived-heading" type="button" @click="showArchived = !showArchived">
              <Archive :size="14" />
              <span>{{ t('channels.archivedGroup', { count: archivedInstances.length }) }}</span>
              <ChevronDown :size="14" :class="{ rotated: showArchived }" />
            </button>
            <div v-if="showArchived" class="platform-items archived-items">
              <div
                v-for="instance in archivedInstances"
                :key="instance.id"
                class="platform-item archived"
                :class="{ selected: matchesInstance(instance) }"
                role="button"
                tabindex="0"
                @click="void selectInstance(instance)"
                @keydown.enter="void selectInstance(instance)"
                @keydown.space.prevent="void selectInstance(instance)"
              >
                <ChannelIcon :icon="descriptorFor(instance)?.icon || ''" :size="22" muted />
                <div class="platform-copy">
                  <div class="platform-name-row"><span class="platform-name">{{ displayName(instance) }}</span><span class="status-dot" :class="statusClass(instance.status)" /></div>
                  <p>{{ projectName(instance) }}</p>
                </div>
                <span class="readonly-mark">{{ t('channels.archivedReadonly') }}</span>
              </div>
            </div>
          </section>

          <div v-if="!filteredActiveInstances.length && !archivedInstances.length" class="platform-empty">
            <Info :size="22" />
            <p>{{ t('channels.empty') }}</p>
          </div>
        </div>
      </aside>

      <main v-if="draft" class="channel-config">
        <header class="config-header">
          <div class="config-header-main">
            <div class="config-icon"><ChannelIcon :icon="selectedDescriptor?.icon || ''" :size="28" :muted="draft.archived" /></div>
            <div class="config-heading-copy">
              <div class="config-title-row">
                <h1>{{ displayName(draft) }}</h1>
                <span class="state-badge" :class="statusClass(activeStatus)">{{ statusLabel(activeStatus) }}</span>
                <span class="state-badge" :class="draft.enabled ? 'enabled' : 'disabled'">{{ draft.enabled ? t('channels.enabled', 'Enabled') : t('channels.disabled', 'Disabled') }}</span>
              </div>
              <p>{{ selectedDescriptor?.description || draft.type }}</p>
            </div>
          </div>
          <div class="auto-save-toggle">
            <span>{{ savingId === draft.id ? t('common.saving', 'Saving...') : t('channels.autoSaveHint', 'Auto-saved after changes') }}</span>
            <button class="channel-switch large" :class="{ on: draft.enabled }" type="button" role="switch" :aria-checked="draft.enabled" :disabled="draft.archived" @click="void toggleEnabled(!draft.enabled)"><span /></button>
          </div>
          <div class="config-meta">
            <span class="meta-badge"><span>{{ t('channels.platform', 'Platform') }}</span>{{ selectedDescriptor?.displayName || draft.type }}</span>
            <span class="meta-badge" :class="{ accent: selectedProject }"><FolderOpen :size="12" /><span>{{ selectedProject?.name || t('channels.unbound') }}</span></span>
            <span class="meta-badge"><span>{{ t('channels.modelShort', 'Model') }}</span>{{ t('channels.modelDefault', 'Client default') }}</span>
            <span v-if="draft.archived" class="meta-badge readonly"><Archive :size="12" />{{ t('channels.archivedReadonly') }}</span>
          </div>
        </header>

        <div class="config-scroll">
          <section class="setting-section setting-row-layout name-section">
            <div class="setting-label">
              <div class="setting-label-line"><label for="channel-name">{{ t('channels.fields.name', 'Channel Name') }}</label><code>name</code></div>
              <p>{{ t('channels.fields.nameHint', 'Used to identify this chat channel within the project.') }}</p>
            </div>
            <input id="channel-name" v-model="draft.name" class="config-input" :disabled="draft.archived" :placeholder="selectedDescriptor?.displayName || draft.type" @input="scheduleSave" />
          </section>

          <div class="config-fields-section">
            <section class="setting-row-layout config-field-row project-binding-row">
              <div class="setting-label">
                <div class="setting-label-line"><label for="channel-project">{{ t('channels.binding.project') }}</label><code>projectId</code></div>
                <p>{{ t('channels.binding.heading') }}</p>
              </div>
              <div>
                <select id="channel-project" v-model="draft.projectId" class="config-input config-select" :disabled="draft.archived" @change="handleProjectChange">
                  <option :value="null">{{ t('channels.binding.none') }}</option>
                  <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.name || project.path }}</option>
                </select>
                <p v-if="selectedProject" class="input-hint">{{ selectedProject.path }}</p>
                <p v-else class="input-warning"><CircleAlert :size="13" />{{ t('channels.binding.warning') }}</p>
              </div>
            </section>

            <section v-for="field in selectedDescriptor?.configSchema || []" :key="field.key" class="setting-row-layout config-field-row">
              <div class="setting-label">
                <div class="setting-label-line"><label :for="`channel-field-${field.key}`">{{ field.label }}<span v-if="field.required" class="required-mark">*</span></label><code>{{ field.key }}</code></div>
                <p v-if="field.placeholder">{{ field.placeholder }}</p>
              </div>
              <div class="secret-input-wrap">
                <input :id="`channel-field-${field.key}`" v-model="draft.config[field.key]" class="config-input" :type="field.secret && !secretVisibility[field.key] ? 'password' : 'text'" :placeholder="field.placeholder || ''" :disabled="draft.archived" @input="scheduleSave" />
                <button v-if="field.secret" class="secret-toggle" type="button" :title="secretVisibility[field.key] ? t('channels.actions.hideSecret', 'Hide secret') : t('channels.actions.showSecret', 'Show secret')" :disabled="draft.archived" @click="toggleSecret(field.key)">
                  <EyeOff v-if="secretVisibility[field.key]" :size="15" /><Eye v-else :size="15" />
                </button>
              </div>
            </section>
          </div>

          <section class="setting-section setting-row-layout inherited-model-row">
            <div class="setting-label">
              <div class="setting-label-line"><label>{{ t('channels.agent.model', 'Reply Model') }}</label><code>client.default.model</code></div>
              <p>{{ t('channels.modelHint', 'Uses the client default model through the local Relay.') }}</p>
            </div>
            <div class="inherited-value"><span>{{ t('channels.modelDefault', 'Client default') }}</span><span class="inherited-value-dot" /></div>
          </section>

          <section class="setting-section setting-row-layout">
            <div class="setting-label">
              <div class="setting-label-line"><label for="channel-prompt">{{ t('channels.agent.prompt') }}</label><code>systemPrompt</code></div>
              <p>{{ t('channels.agent.promptHint', 'Optional instructions that shape this channel Agent persona.') }}</p>
            </div>
            <textarea id="channel-prompt" v-model="draft.config.systemPrompt" class="config-input config-textarea" rows="4" :disabled="draft.archived" :placeholder="t('channels.agent.promptPlaceholder')" @input="scheduleSave" />
          </section>

          <section class="advanced-section">
            <div class="advanced-heading">
              <div><h2>{{ t('channels.advanced.title', 'Advanced settings') }}</h2><p>{{ t('channels.advanced.description', 'Reply strategy, tool capabilities, and permission boundaries.') }}</p></div>
              <span class="outline-badge">{{ t('channels.advanced.collapsible', 'Collapsible') }}</span>
            </div>

            <details open class="accordion-section">
              <summary><div><strong>{{ t('channels.features.title', 'Features') }}</strong><p>{{ t('channels.features.description', 'Auto-reply, streaming reply, and auto-start policies.') }}</p></div><ChevronDown :size="15" /></summary>
              <div class="accordion-content">
                <div class="advanced-setting"><div><strong>{{ t('channels.features.autoReply') }}</strong><p>{{ t('channels.features.autoReplyHint') }}</p></div><button class="channel-switch" :class="{ on: draft.features.autoReply }" type="button" role="switch" :aria-checked="draft.features.autoReply" :disabled="draft.archived" @click="setFeature('autoReply', !draft.features.autoReply)"><span /></button></div>
                <div class="advanced-setting"><div><strong>{{ t('channels.features.streaming') }}</strong><p>{{ t('channels.features.streamingHint') }}</p></div><button class="channel-switch" :class="{ on: draft.features.streamingReply }" type="button" role="switch" :aria-checked="draft.features.streamingReply" :disabled="draft.archived" @click="setFeature('streamingReply', !draft.features.streamingReply)"><span /></button></div>
                <div class="advanced-setting"><div><strong>{{ t('channels.features.autoStart') }}</strong><p>{{ t('channels.features.autoStartHint') }}</p></div><button class="channel-switch" :class="{ on: draft.features.autoStart }" type="button" role="switch" :aria-checked="draft.features.autoStart" :disabled="draft.archived" @click="setFeature('autoStart', !draft.features.autoStart)"><span /></button></div>
              </div>
            </details>

            <details v-if="selectedDescriptor?.tools?.length" class="accordion-section">
              <summary><div><strong>{{ t('channels.tools.title') }}</strong><p>{{ t('channels.tools.description', 'Control the tools available to this channel.') }}</p></div><ChevronDown :size="15" /></summary>
              <div class="accordion-content">
                <div v-for="tool in selectedDescriptor.tools" :key="tool" class="advanced-setting"><div><strong>{{ tool }}</strong></div><button class="channel-switch" :class="{ on: draft.tools[tool] !== false }" type="button" role="switch" :aria-checked="draft.tools[tool] !== false" :disabled="draft.archived" @click="setTool(tool, draft.tools[tool] === false)"><span /></button></div>
              </div>
            </details>

            <details class="accordion-section">
              <summary><div class="summary-with-icon"><Shield :size="15" /><div><strong>{{ t('channels.permissions.title') }}</strong><p>{{ t('channels.permissions.description', 'Restrict channel read/write scope and command capabilities.') }}</p></div></div><ChevronDown :size="15" /></summary>
              <div class="accordion-content">
                <div class="advanced-setting"><div><strong>{{ t('channels.permissions.readHome') }}</strong><p>{{ t('channels.permissions.readHomeHint', 'Allow reading files under the user home directory.') }}</p></div><button class="channel-switch" :class="{ on: draft.permissions.allowReadHome }" type="button" role="switch" :aria-checked="draft.permissions.allowReadHome" :disabled="draft.archived" @click="setPermission('allowReadHome', !draft.permissions.allowReadHome)"><span /></button></div>
                <div v-if="!draft.permissions.allowReadHome" class="read-paths-setting"><div><strong>{{ t('channels.permissions.readablePaths', 'Allowed Read Paths') }}</strong><p>{{ t('channels.permissions.readablePathsHint', 'Whitelist directories the channel can read.') }}</p></div><div v-if="draft.permissions.readablePathPrefixes.length" class="path-tags"><span v-for="path in draft.permissions.readablePathPrefixes" :key="path" class="path-tag">{{ path }}<button type="button" :title="t('channels.actions.removePath', 'Remove path')" :disabled="draft.archived" @click="removeReadablePath(path)"><X :size="11" /></button></span></div><div class="path-input-row"><input v-model="readablePathDraft" class="config-input" :disabled="draft.archived" placeholder="C:\\Work\\Project" @keydown.enter.prevent="addReadablePath" /><button type="button" class="outline-action" :disabled="draft.archived" @click="addReadablePath">{{ t('channels.permissions.addPath', 'Add') }}</button></div></div>
                <div class="advanced-setting"><div><strong>{{ t('channels.permissions.shell') }}</strong><p>{{ t('channels.permissions.shellHint', 'Allow executing terminal commands.') }}</p></div><button class="channel-switch" :class="{ on: draft.permissions.allowShell }" type="button" role="switch" :aria-checked="draft.permissions.allowShell" :disabled="draft.archived" @click="setPermission('allowShell', !draft.permissions.allowShell)"><span /></button></div>
                <div class="advanced-setting"><div><strong>{{ t('channels.permissions.writeOutside') }}</strong><p>{{ t('channels.permissions.writeOutsideHint', 'Allow writing files outside the bound project.') }}</p></div><button class="channel-switch" :class="{ on: draft.permissions.allowWriteOutside }" type="button" role="switch" :aria-checked="draft.permissions.allowWriteOutside" :disabled="draft.archived" @click="setPermission('allowWriteOutside', !draft.permissions.allowWriteOutside)"><span /></button></div>
                <div class="advanced-setting"><div><strong>{{ t('channels.permissions.subAgents') }}</strong><p>{{ t('channels.permissions.subAgentsHint', 'Allow channel Agent sub-agent tools.') }}</p></div><button class="channel-switch" :class="{ on: draft.permissions.allowSubAgents }" type="button" role="switch" :aria-checked="draft.permissions.allowSubAgents" :disabled="draft.archived" @click="setPermission('allowSubAgents', !draft.permissions.allowSubAgents)"><span /></button></div>
              </div>
            </details>
           </section>

          <section v-if="isWeixinInstance(draft)" class="weixin-binding-section">
            <div class="weixin-binding-header">
              <div class="setting-label">
                <div class="setting-label-line"><label>{{ t('channels.weixin.binding') }}</label><code>qrLogin</code></div>
                <p>{{ t('channels.weixin.bindingHint') }}</p>
              </div>
              <div class="weixin-binding-actions">
                <button v-if="weixinQrUrl" class="outline-action" type="button" :disabled="draft.archived || weixinLoginPending" @click="void bindWeixin()"><RefreshCw :size="14" :class="{ spinning: weixinLoginPending }" />{{ t('channels.weixin.refreshQr') }}</button>
                <button class="outline-action" type="button" :disabled="draft.archived || weixinLoginPending" @click="void bindWeixin()"><RefreshCw :size="14" :class="{ spinning: weixinLoginPending }" />{{ weixinLoginPending ? t('channels.weixin.bindingInProgress') : draft.config.token ? t('channels.weixin.rebind') : t('channels.weixin.bind') }}</button>
              </div>
            </div>
            <div class="weixin-binding-card">
              <div class="weixin-binding-state" :class="weixinStatusClass(weixinLoginStatus)"><span class="weixin-state-dot" />{{ draft.config.token ? t('channels.weixin.bound') : t('channels.weixin.unbound') }}</div>
              <p v-if="weixinLoginMessage" class="input-hint">{{ weixinLoginMessage }}</p>
              <p v-else class="input-hint">{{ t('channels.weixin.scanHint') }}</p>
              <div v-if="weixinQrUrl" class="weixin-qr-shell"><img :src="weixinQrUrl" :alt="t('channels.weixin.qrAlt')" /></div>
              <p v-if="!draft.projectId && draft.config.token" class="input-warning"><CircleAlert :size="13" />{{ t('channels.weixin.bindProjectBeforeStart') }}</p>
            </div>
          </section>

        </div>

        <footer class="config-footer">
          <div class="footer-error" v-if="draft.lastError"><CircleAlert :size="14" />{{ draft.lastError }}</div>
          <button v-if="isCustom(draft)" class="danger-action" type="button" :disabled="savingId === draft.id" @click="remove"><Trash2 :size="14" />{{ t('channels.actions.remove', 'Remove') }}</button>
          <div class="footer-actions">
            <button v-if="!draft.archived && activeStatus === 'running'" class="outline-action" type="button" @click="stop"><Square :size="14" />{{ t('channels.actions.stop') }}</button>
            <button v-else-if="!draft.archived" class="outline-action" type="button" :disabled="!draft.projectId" @click="start"><Play :size="14" />{{ t('channels.actions.start') }}</button>
          </div>
        </footer>
      </main>

      <div v-else class="empty-config"><Info :size="22" />{{ t('channels.empty') }}</div>
    </div>
    </div>
  </section>
</template>

<style scoped>
/* 对齐原版项目页：页面壳固定，滚动只下沉到左右工作区，避免项目上下文随配置内容离开视口。 */
.channels-page { display: flex; width: 100%; height: 100%; min-width: 0; min-height: 0; box-sizing: border-box; flex: 1 1 auto; flex-direction: column; overflow: hidden; padding: 16px 24px 24px; background: var(--mac-bg); color: var(--mac-text); }
.channels-content { display: flex; width: 100%; min-width: 0; min-height: 0; flex: 1 1 auto; flex-direction: column; overflow: hidden; }
.channels-context { display: flex; width: 100%; max-width: 1480px; box-sizing: border-box; flex: 0 0 auto; align-items: flex-end; justify-content: space-between; gap: 16px; margin: 0 auto; padding: 0 0 16px; }
.channels-context-copy { min-width: 0; }
.channels-context-eyebrow { margin: 0; color: var(--mac-text-secondary); font-size: 11px; font-weight: 650; letter-spacing: .08em; text-transform: uppercase; }
.channels-context-copy h1 { margin: 4px 0 0; overflow: hidden; color: var(--mac-text); font-size: 14px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.channels-context-copy > p:last-child { max-width: 880px; margin: 4px 0 0; overflow: hidden; color: var(--mac-text-secondary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.channels-context-actions { display: flex; min-width: max-content; flex: 0 0 auto; gap: 8px; }
.channels-workspace { display: grid; width: 100%; max-width: 1480px; min-height: 0; flex: 1; grid-template-columns: 320px minmax(0, 1fr); overflow: hidden; margin: 0 auto; border: 1px solid var(--mac-border); border-radius: 6px; background: var(--mac-surface); }
.channels-error { display: flex; width: 100%; max-width: 1480px; box-sizing: border-box; align-items: center; gap: 8px; margin: 0 auto 16px; padding: 10px 12px; border: 1px solid rgba(218, 75, 57, .24); border-radius: 7px; background: rgba(218, 75, 57, .08); color: #a33a2f; font-size: 12px; }
.channels-error button { margin-left: auto; border: 0; background: transparent; color: inherit; font: inherit; font-weight: 650; cursor: pointer; }
.channels-state { display: flex; min-height: 260px; flex: 1; align-items: center; justify-content: center; gap: 8px; color: var(--mac-text-secondary); font-size: 13px; }
.empty-config { display: flex; min-height: 260px; flex: 1; align-items: center; justify-content: center; gap: 8px; color: var(--mac-text-secondary); font-size: 13px; }
.platforms-panel { display: flex; min-height: 0; flex-direction: column; border-right: 1px solid var(--mac-border); background: color-mix(in srgb, var(--mac-surface-strong) 58%, transparent); }
.platforms-header { padding: 20px 16px 14px; border-bottom: 1px solid var(--mac-border); }
.platforms-title { margin: 0; color: var(--mac-text-secondary); font-size: 11px; font-weight: 650; letter-spacing: .16em; text-transform: uppercase; }
.platform-search { display: flex; align-items: center; gap: 8px; height: 34px; margin-top: 13px; padding: 0 10px; border: 1px solid var(--mac-border); border-radius: 7px; background: var(--mac-surface); color: var(--mac-text-secondary); }
.platform-search input { min-width: 0; flex: 1; border: 0; outline: 0; background: transparent; color: var(--mac-text); font: inherit; font-size: 12px; }
.platform-search input::placeholder { color: var(--mac-text-secondary); opacity: .75; }
.platforms-scroll { min-height: 0; flex: 1; overflow-y: auto; padding: 12px; }
.platform-category { margin-bottom: 16px; }
.category-label { margin: 0 8px 8px; color: var(--mac-text-secondary); font-size: 10px; font-weight: 650; letter-spacing: .18em; text-transform: uppercase; }
.platform-items { display: flex; flex-direction: column; gap: 6px; }
.platform-item { display: flex; min-width: 0; align-items: center; gap: 12px; padding: 12px; border: 1px solid transparent; border-radius: 16px; color: var(--mac-text); cursor: pointer; transition: background .15s ease, border-color .15s ease; }
.platform-item:hover { background: color-mix(in srgb, var(--mac-accent) 7%, transparent); }
.platform-item.selected { border-color: color-mix(in srgb, var(--mac-accent) 26%, var(--mac-border)); background: color-mix(in srgb, var(--mac-accent) 11%, var(--mac-surface)); }
.platform-item.disabled { color: var(--mac-text-secondary); }
.platform-item.archived { color: var(--mac-text-secondary); opacity: .82; }
.platform-copy { min-width: 0; flex: 1; }
.platform-name-row { display: flex; min-width: 0; align-items: center; gap: 7px; }
.platform-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 560; }
.bound-mark { display: inline-flex; min-width: 0; align-items: center; gap: 3px; color: var(--mac-accent); font-size: 9px; font-weight: 650; white-space: nowrap; }
.bound-mark.account-bound { color: #2f7c5b; }
.platform-copy p { overflow: hidden; margin: 4px 0 0; color: var(--mac-text-secondary); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.status-dot { width: 6px; height: 6px; flex: 0 0 6px; border-radius: 50%; background: color-mix(in srgb, var(--mac-text-secondary) 34%, transparent); }
.status-dot.status-running { background: #24a148; }
.status-dot.status-error { background: #da4b39; }
.status-dot.status-starting { background: #d89000; }
.channel-switch { position: relative; width: 32px; height: 18px; flex: 0 0 32px; padding: 0; border: 0; border-radius: 999px; background: color-mix(in srgb, var(--mac-text-secondary) 30%, transparent); cursor: pointer; transition: background .15s ease; }
.channel-switch span { position: absolute; top: 3px; left: 3px; width: 12px; height: 12px; border-radius: 50%; background: var(--mac-surface); box-shadow: 0 1px 3px rgba(0, 0, 0, .22); transition: transform .15s ease; }
.channel-switch.on { background: var(--mac-accent); }
.channel-switch.on span { transform: translateX(14px); }
.channel-switch.large { width: 36px; height: 20px; flex-basis: 36px; }
.channel-switch.large span { width: 14px; height: 14px; }
.channel-switch.large.on span { transform: translateX(16px); }
.channel-switch:disabled { cursor: not-allowed; opacity: .48; }
.archived-category { margin: 10px 0 0; padding-top: 12px; border-top: 1px solid var(--mac-divider); }
.archived-heading { display: flex; width: 100%; align-items: center; gap: 7px; padding: 7px 9px; border: 0; border-radius: 7px; background: transparent; color: var(--mac-text-secondary); font: inherit; font-size: 11px; font-weight: 650; text-align: left; cursor: pointer; }
.archived-heading span { min-width: 0; flex: 1; }
.archived-heading:hover { background: color-mix(in srgb, var(--mac-accent) 7%, transparent); color: var(--mac-text); }
.archived-heading svg:last-child { transition: transform .15s ease; }
.archived-heading svg.rotated { transform: rotate(180deg); }
.archived-items { margin-top: 4px; }
.readonly-mark { flex: 0 0 auto; color: var(--mac-text-secondary); font-size: 9px; }
.platform-empty { display: flex; min-height: 220px; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--mac-text-secondary); text-align: center; }
.platform-empty p { margin: 0; font-size: 13px; }
.channel-config { display: flex; min-width: 0; min-height: 0; flex-direction: column; background: var(--mac-surface); }
.config-header { display: grid; flex: 0 0 auto; grid-template-columns: minmax(0, 1fr) auto; gap: 16px; padding: 20px 24px; border-bottom: 1px solid var(--mac-border); }
.config-header-main { display: flex; min-width: 0; align-items: center; gap: 16px; }
.config-icon { display: inline-flex; width: 44px; height: 44px; flex: 0 0 44px; align-items: center; justify-content: center; border: 1px solid var(--mac-border); border-radius: 16px; background: color-mix(in srgb, var(--mac-surface-strong) 58%, transparent); }
.config-heading-copy { min-width: 0; }
.config-title-row { display: flex; min-width: 0; align-items: center; flex-wrap: wrap; gap: 7px; }
.config-title-row h1 { min-width: 0; max-width: min(520px, 100%); margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 20px; font-weight: 650; letter-spacing: 0; }
.config-heading-copy > p { overflow: hidden; margin: 4px 0 0; color: var(--mac-text-secondary); font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.state-badge, .meta-badge, .outline-badge { display: inline-flex; min-height: 21px; align-items: center; gap: 5px; padding: 0 7px; border: 1px solid var(--mac-border); border-radius: 5px; color: var(--mac-text-secondary); background: var(--mac-surface); font-size: 10px; font-weight: 600; white-space: nowrap; }
.state-badge.status-running { border-color: rgba(36, 161, 72, .25); background: rgba(36, 161, 72, .1); color: #176b32; }
.state-badge.status-error { border-color: rgba(218, 75, 57, .25); background: rgba(218, 75, 57, .1); color: #a33a2f; }
.state-badge.enabled { color: var(--mac-accent); }
.state-badge.disabled { color: var(--mac-text-secondary); background: var(--mac-surface-strong); }
.auto-save-toggle { display: flex; align-items: center; gap: 12px; color: var(--mac-text-secondary); font-size: 12px; white-space: nowrap; }
.config-meta { display: flex; min-width: 0; grid-column: 1 / -1; flex-wrap: wrap; gap: 6px; }
.meta-badge { max-width: 100%; overflow: hidden; }
.meta-badge.accent { border-color: color-mix(in srgb, var(--mac-accent) 28%, var(--mac-border)); background: color-mix(in srgb, var(--mac-accent) 8%, transparent); color: var(--mac-text); }
.meta-badge.readonly { color: var(--mac-text-secondary); }
.config-scroll { min-height: 0; flex: 1; overflow-y: auto; padding: 20px 24px 24px; }
.setting-section { border-bottom: 1px solid var(--mac-divider); padding: 20px 0; }
.name-section { padding-top: 0; }
.config-fields-section { border-bottom: 1px solid var(--mac-divider); padding: 20px 0; }
.config-field-row { padding: 0; }
.config-field-row + .config-field-row { margin-top: 16px; }
.project-binding-row { padding-bottom: 16px; border-bottom: 1px solid var(--mac-divider); }
.inherited-model-row { border-bottom: 1px solid var(--mac-divider); }
.inherited-value { display: inline-flex; min-height: 40px; align-items: center; gap: 8px; padding: 0 12px; border: 1px solid var(--mac-border); border-radius: 6px; background: var(--mac-surface-strong); color: var(--mac-text-secondary); font-size: 13px; }
.inherited-value-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--mac-accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent) 15%, transparent); }
.setting-row-layout { display: grid; grid-template-columns: 220px minmax(0, 1fr); gap: 18px; align-items: start; }
.setting-label { min-width: 0; }
.setting-label-line { display: flex; min-width: 0; align-items: center; gap: 7px; }
.setting-label label { font-size: 14px; font-weight: 600; }
.setting-label code { padding: 2px 5px; border: 1px solid var(--mac-border); border-radius: 4px; color: var(--mac-text-secondary); background: var(--mac-surface-strong); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 9px; }
.setting-label p, .input-hint, .input-warning { margin: 6px 0 0; color: var(--mac-text-secondary); font-size: 12px; line-height: 1.45; }
.input-warning { display: flex; align-items: center; gap: 5px; color: #b35f22; }
.config-input { display: block; width: 100%; min-height: 40px; box-sizing: border-box; padding: 8px 12px; border: 1px solid var(--mac-border); border-radius: 6px; outline: 0; background: var(--mac-surface-strong); color: var(--mac-text); font: inherit; font-size: 14px; transition: border-color .15s ease, box-shadow .15s ease; }
.config-input:focus { border-color: var(--mac-accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent) 18%, transparent); }
.config-input:disabled { cursor: not-allowed; opacity: .58; }
.config-select { appearance: auto; }
.config-textarea { min-height: 88px; resize: vertical; line-height: 1.45; }
.input-hint { overflow-wrap: anywhere; }
.input-warning { display: flex; align-items: center; gap: 5px; color: #b35f22; }
.weixin-binding-section { display: block; padding: 20px 0; border-bottom: 1px solid var(--mac-divider); }
.weixin-binding-header { display: flex; min-width: 0; align-items: flex-start; justify-content: space-between; gap: 18px; }
.weixin-binding-header > .setting-label { min-width: 0; flex: 1; }
.weixin-binding-actions { display: flex; min-width: max-content; flex: 0 0 auto; flex-wrap: nowrap; justify-content: flex-end; gap: 7px; }
.weixin-binding-actions .outline-action { flex: 0 0 auto; white-space: nowrap; }
.weixin-binding-card { margin-top: 12px; padding: 12px; border: 1px solid var(--mac-border); border-radius: 6px; background: color-mix(in srgb, var(--mac-surface-strong) 48%, transparent); }
.weixin-binding-state { display: inline-flex; align-items: center; gap: 7px; color: var(--mac-text-secondary); font-size: 12px; font-weight: 650; }
.weixin-binding-state.weixin-status-confirmed { color: #24764c; }
.weixin-binding-state.weixin-status-error { color: #b33e32; }
.weixin-state-dot { width: 7px; height: 7px; border-radius: 50%; background: color-mix(in srgb, var(--mac-text-secondary) 45%, transparent); }
.weixin-status-confirmed .weixin-state-dot { background: #24a148; }
.weixin-status-starting .weixin-state-dot, .weixin-status-wait .weixin-state-dot, .weixin-status-scaned .weixin-state-dot { background: #d89000; }
.weixin-status-error .weixin-state-dot { background: #da4b39; }
.weixin-qr-shell { display: flex; width: 100%; margin-top: 12px; align-items: center; justify-content: center; padding: 12px; box-sizing: border-box; border: 1px solid var(--mac-border); border-radius: 6px; background: #fff; }
.weixin-qr-shell img { display: block; width: auto; max-width: 100%; height: auto; max-height: 420px; object-fit: contain; }
.secret-input-wrap { position: relative; min-width: 0; }
.secret-input-wrap .config-input { padding-right: 40px; }
.secret-toggle { position: absolute; top: 4px; right: 4px; display: inline-flex; width: 28px; height: 28px; align-items: center; justify-content: center; border: 0; border-radius: 5px; background: transparent; color: var(--mac-text-secondary); cursor: pointer; }
.secret-toggle:hover:not(:disabled) { background: color-mix(in srgb, var(--mac-accent) 8%, transparent); color: var(--mac-text); }
.secret-toggle:disabled { cursor: not-allowed; opacity: .5; }
.required-mark { margin-left: 3px; color: #da4b39; }
.advanced-section { padding: 20px 0 4px; border-bottom: 1px solid var(--mac-divider); }
.advanced-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 5px; }
.advanced-heading h2 { margin: 0; font-size: 14px; font-weight: 650; letter-spacing: 0; }
.advanced-heading p { margin: 5px 0 0; color: var(--mac-text-secondary); font-size: 12px; }
.outline-badge { color: var(--mac-text-secondary); }
.accordion-section { border-top: 1px solid var(--mac-divider); }
.accordion-section summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 0; list-style: none; cursor: pointer; }
.accordion-section summary::-webkit-details-marker { display: none; }
.accordion-section summary > svg { flex: 0 0 auto; color: var(--mac-text-secondary); transition: transform .15s ease; }
.accordion-section[open] summary > svg { transform: rotate(180deg); }
.accordion-section summary strong { font-size: 14px; font-weight: 600; }
.accordion-section summary p { margin: 4px 0 0; color: var(--mac-text-secondary); font-size: 12px; }
.summary-with-icon { display: flex; align-items: flex-start; gap: 8px; }
.summary-with-icon > svg { margin-top: 1px; color: var(--mac-text-secondary); }
.accordion-content { display: flex; flex-direction: column; gap: 12px; padding: 0 0 13px; }
.advanced-setting, .read-paths-setting { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border: 1px solid var(--mac-border); border-radius: 12px; }
.advanced-setting > div, .read-paths-setting > div:first-child { min-width: 0; }
.advanced-setting strong, .read-paths-setting strong { font-size: 14px; font-weight: 600; }
.advanced-setting p, .read-paths-setting p { margin: 4px 0 0; color: var(--mac-text-secondary); font-size: 12px; line-height: 1.4; }
.read-paths-setting { display: block; }
.path-tags { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 9px; }
.path-tag { display: inline-flex; max-width: 100%; align-items: center; gap: 5px; padding: 3px 6px; border-radius: 4px; background: var(--mac-surface-strong); color: var(--mac-text-secondary); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 9px; overflow-wrap: anywhere; }
.path-tag button { display: inline-flex; padding: 0; border: 0; background: transparent; color: inherit; cursor: pointer; }
.path-input-row { display: flex; gap: 7px; margin-top: 9px; }
.path-input-row .config-input { min-width: 0; flex: 1; }
.outline-action, .primary-action, .danger-action { display: inline-flex; min-height: 32px; min-width: max-content; flex: 0 0 auto; align-items: center; justify-content: center; gap: 6px; padding: 0 12px; border: 1px solid var(--mac-border); border-radius: 6px; background: var(--mac-surface); color: var(--mac-text); font: inherit; font-size: 12px; font-weight: 600; white-space: nowrap; cursor: pointer; }
.outline-action:hover:not(:disabled) { border-color: color-mix(in srgb, var(--mac-accent) 38%, var(--mac-border)); background: color-mix(in srgb, var(--mac-accent) 6%, transparent); }
.primary-action { border-color: var(--mac-accent); background: var(--mac-accent); color: #fff; }
.primary-action:hover:not(:disabled) { filter: brightness(.96); }
.danger-action { border-color: transparent; background: transparent; color: #c24135; }
.danger-action:hover:not(:disabled) { background: rgba(218, 75, 57, .08); }
.outline-action:disabled, .primary-action:disabled, .danger-action:disabled { cursor: not-allowed; opacity: .48; }
.icon-only { width: 34px; padding: 0; }
.context-action { min-height: 32px; white-space: nowrap; }
.config-footer { display: flex; min-height: 56px; flex: 0 0 auto; align-items: center; justify-content: space-between; gap: 12px; padding: 16px 24px; border-top: 1px solid var(--mac-border); }
.footer-error { display: flex; min-width: 0; align-items: center; gap: 6px; overflow: hidden; color: #b33e32; font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.footer-actions { display: flex; gap: 7px; margin-left: auto; }
.spinning { animation: channel-spin .8s linear infinite; }
@keyframes channel-spin { to { transform: rotate(360deg); } }
@media (max-width: 980px) { .channels-workspace { grid-template-columns: 250px minmax(0, 1fr); } .config-header { padding-left: 18px; padding-right: 18px; } .config-scroll { padding: 20px 18px 24px; } .config-footer { padding: 16px 18px; } .setting-row-layout { grid-template-columns: 160px minmax(0, 1fr); } }
@media (max-width: 760px) { .channels-page { padding: 16px 16px 20px; } .channels-context { align-items: flex-start; flex-direction: column; gap: 12px; } .channels-context-actions { flex-wrap: wrap; } .channels-workspace { display: flex; min-height: 0; flex-direction: column; overflow: visible; } .platforms-panel { flex: 0 0 auto; border-right: 0; border-bottom: 1px solid var(--mac-border); } .platforms-scroll { max-height: 280px; } .channel-config { min-height: 680px; } .config-header { grid-template-columns: 1fr; } .auto-save-toggle { justify-content: space-between; } .config-meta { grid-column: auto; } .weixin-binding-header { display: block; } .weixin-binding-actions { justify-content: flex-start; margin-top: 12px; flex-wrap: wrap; } }
@media (max-width: 560px) { .setting-row-layout { display: block; } .setting-label { margin-bottom: 10px; } .config-footer { align-items: flex-start; flex-direction: column; } .footer-actions { width: 100%; margin-left: 0; } .footer-actions > button { flex: 1; } }
</style>
