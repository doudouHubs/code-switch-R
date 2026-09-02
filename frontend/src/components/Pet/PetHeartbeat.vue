<script setup lang="ts">
import { Events } from '../../wails-runtime-compat'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Activity, Clock3, HeartPulse, Play, RefreshCw, Save, Square } from '@lucide/vue'
import { extractErrorMessage } from '../../utils/error'
import { showToast } from '../../utils/toast'
import {
  DEFAULT_PET_ID,
  type PetHeartbeatConfig,
  type PetHeartbeatPhase,
  type PetHeartbeatSnapshot
} from './petTypes'
import {
  normalizePetHeartbeatEvent,
  petHeartbeatApi
} from './petHeartbeatApi'

const PET_HEARTBEAT_MIN_INTERVAL = 1
const PET_HEARTBEAT_MAX_INTERVAL = 1440
const PET_HEARTBEAT_MAX_PROMPT_LENGTH = 16_000

interface PetHeartbeatProps {
  embedded?: boolean
}

const props = withDefaults(defineProps<PetHeartbeatProps>(), {
  embedded: false
})

const { t, locale } = useI18n()

const loading = ref(true)
const saving = ref(false)
const workingAction = ref<'refresh' | 'run' | 'cancel' | null>(null)
const errorMessage = ref('')
const snapshot = ref<PetHeartbeatSnapshot | null>(null)
const now = ref(Date.now())
const draft = reactive<PetHeartbeatConfig>({
  petId: DEFAULT_PET_ID,
  enabled: false,
  intervalMinutes: 30,
  prompt: ''
})
let stopHeartbeatEvents: (() => void) | undefined
let clockTimer: number | undefined

const isDirty = computed(() => {
  const current = snapshot.value?.config
  if (!current) return false
  return current.enabled !== draft.enabled ||
    current.intervalMinutes !== draft.intervalMinutes ||
    current.prompt !== draft.prompt
})

const promptLength = computed(() => Array.from(draft.prompt).length)
const intervalError = computed(() => {
  if (!Number.isInteger(draft.intervalMinutes) ||
    draft.intervalMinutes < PET_HEARTBEAT_MIN_INTERVAL ||
    draft.intervalMinutes > PET_HEARTBEAT_MAX_INTERVAL) {
    return t('pet.heartbeat.validation.interval')
  }
  return ''
})
const promptError = computed(() => {
  if (draft.enabled && !draft.prompt.trim()) return t('pet.heartbeat.validation.promptRequired')
  if (promptLength.value > PET_HEARTBEAT_MAX_PROMPT_LENGTH) return t('pet.heartbeat.validation.promptTooLong')
  return ''
})
const formError = computed(() => intervalError.value || promptError.value)

const phase = computed<PetHeartbeatPhase>(() => snapshot.value?.runtime.phase ?? 'disabled')
const isActive = computed(() => phase.value === 'running' || phase.value === 'waiting_for_idle')
const canRunNow = computed(() => !loading.value && !saving.value && workingAction.value === null && !isActive.value)
const canCancel = computed(() => !loading.value && !saving.value && workingAction.value === null && isActive.value)

const phaseLabel = computed(() => t(`pet.heartbeat.phase.${phase.value}`))
const lastStatusLabel = computed(() => {
  const status = snapshot.value?.runtime.lastStatus ?? 'none'
  return t(`pet.heartbeat.lastStatus.${status}`)
})
const nextRunLabel = computed(() => {
  const timestamp = snapshot.value?.runtime.nextRunAt ?? 0
  if (phase.value !== 'waiting' || timestamp <= 0) return t('pet.heartbeat.notScheduled')
  const remaining = Math.max(0, timestamp - now.value)
  return remaining > 0 ? formatCountdown(remaining) : t('pet.heartbeat.due')
})
const lastFinishedLabel = computed(() => formatTimestamp(snapshot.value?.runtime.lastFinishedAt ?? 0))
const statusTone = computed(() => {
  if (phase.value === 'running') return 'running'
  if (phase.value === 'waiting_for_idle') return 'waiting'
  if (phase.value === 'waiting') return 'scheduled'
  return 'disabled'
})

function assignDraft(config: PetHeartbeatConfig): void {
  draft.petId = config.petId || DEFAULT_PET_ID
  draft.enabled = config.enabled
  draft.intervalMinutes = config.intervalMinutes
  draft.prompt = config.prompt
}

function applySnapshot(next: PetHeartbeatSnapshot, syncDraft = false): void {
  snapshot.value = next
  if (syncDraft || !isDirty.value) assignDraft(next.config)
}

function unwrapEventData(value: unknown): unknown {
  return Array.isArray(value) && value.length === 1 ? value[0] : value
}

function handleHeartbeatEvent(value: unknown): void {
  const event = normalizePetHeartbeatEvent(unwrapEventData(value))
  if (event) applySnapshot(event.snapshot)
}

async function loadSnapshot(syncDraft = true): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    applySnapshot(await petHeartbeatApi.getSnapshot(), syncDraft)
  } catch (error) {
    errorMessage.value = extractErrorMessage(error, t('pet.heartbeat.errors.load'))
  } finally {
    loading.value = false
  }
}

async function saveConfig(): Promise<void> {
  if (saving.value || loading.value || formError.value || !isDirty.value) return
  saving.value = true
  errorMessage.value = ''
  try {
    const next = await petHeartbeatApi.saveConfig({ ...draft })
    applySnapshot(next, true)
    showToast(t('pet.heartbeat.feedback.saved'), 'success')
  } catch (error) {
    errorMessage.value = extractErrorMessage(error, t('pet.heartbeat.errors.save'))
  } finally {
    saving.value = false
  }
}

async function runNow(): Promise<void> {
  if (!canRunNow.value) return
  workingAction.value = 'run'
  errorMessage.value = ''
  try {
    applySnapshot(await petHeartbeatApi.runNow())
    showToast(t('pet.heartbeat.feedback.runStarted'), 'info')
  } catch (error) {
    errorMessage.value = extractErrorMessage(error, t('pet.heartbeat.errors.run'))
  } finally {
    workingAction.value = null
  }
}

async function cancel(): Promise<void> {
  if (!canCancel.value) return
  workingAction.value = 'cancel'
  errorMessage.value = ''
  try {
    applySnapshot(await petHeartbeatApi.cancel())
    showToast(t('pet.heartbeat.feedback.cancelRequested'), 'info')
  } catch (error) {
    errorMessage.value = extractErrorMessage(error, t('pet.heartbeat.errors.cancel'))
  } finally {
    workingAction.value = null
  }
}

async function refresh(): Promise<void> {
  if (workingAction.value !== null || loading.value) return
  workingAction.value = 'refresh'
  try {
    // 刷新只同步后端快照；有未保存编辑时不覆盖草稿，避免一次点击误删提示词。
    await loadSnapshot(!isDirty.value)
  } finally {
    workingAction.value = null
  }
}

function setIntervalPreset(value: number): void {
  draft.intervalMinutes = value
}

function formatCountdown(milliseconds: number): string {
  const totalSeconds = Math.ceil(milliseconds / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60)
    const restMinutes = minutes % 60
    return restMinutes > 0
      ? t('pet.heartbeat.countdown.hoursMinutes', { hours, minutes: restMinutes })
      : t('pet.heartbeat.countdown.hours', { hours })
  }
  if (minutes > 0) return t('pet.heartbeat.countdown.minutesSeconds', { minutes, seconds })
  return t('pet.heartbeat.countdown.seconds', { seconds })
}

function formatTimestamp(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return t('pet.heartbeat.notAvailable')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp))
}

onMounted(() => {
  stopHeartbeatEvents = Events.On('pet.heartbeat', (event) => handleHeartbeatEvent(event.data))
  clockTimer = window.setInterval(() => { now.value = Date.now() }, 1000)
  void loadSnapshot()
})

onUnmounted(() => {
  stopHeartbeatEvents?.()
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
})
</script>

<template>
  <div :class="['pet-heartbeat', { 'pet-heartbeat--embedded': props.embedded }]">
    <header class="pet-heartbeat__header">
      <div class="pet-heartbeat__heading">
        <div class="pet-heartbeat__heading-icon" aria-hidden="true">
          <HeartPulse :size="22" :stroke-width="1.8" />
        </div>
        <div>
          <p class="pet-heartbeat__eyebrow">{{ t('pet.heartbeat.eyebrow') }}</p>
          <h1>{{ t('pet.heartbeat.title') }}</h1>
          <p class="pet-heartbeat__subtitle">{{ t('pet.heartbeat.subtitle') }}</p>
        </div>
      </div>
      <button
        type="button"
        class="pet-heartbeat__icon-button"
        :disabled="workingAction !== null || loading"
        :title="t('pet.heartbeat.refresh')"
        :aria-label="t('pet.heartbeat.refresh')"
        @click="void refresh()"
      >
        <RefreshCw :class="{ 'is-spinning': workingAction === 'refresh' }" :size="16" :stroke-width="1.9" aria-hidden="true" />
      </button>
    </header>

    <div v-if="loading" class="pet-heartbeat__state">
      <Activity class="is-spinning" :size="18" aria-hidden="true" />
      <span>{{ t('pet.heartbeat.loading') }}</span>
    </div>
    <div v-else-if="errorMessage" class="pet-heartbeat__state is-error">
      <span>{{ errorMessage }}</span>
      <button type="button" class="pet-heartbeat__button pet-heartbeat__button--quiet" @click="void loadSnapshot()">
        {{ t('pet.common.retry') }}
      </button>
    </div>

    <div v-else-if="snapshot" class="pet-heartbeat__layout">
      <section class="pet-heartbeat__status-panel" :data-tone="statusTone" aria-labelledby="pet-heartbeat-status-title">
        <div class="pet-heartbeat__panel-header">
          <div>
            <p class="pet-heartbeat__eyebrow">{{ t('pet.heartbeat.statusKicker') }}</p>
            <h2 id="pet-heartbeat-status-title">{{ t('pet.heartbeat.statusTitle') }}</h2>
          </div>
          <span class="pet-heartbeat__phase">
            <span class="pet-heartbeat__phase-dot" aria-hidden="true"></span>
            {{ phaseLabel }}
          </span>
        </div>

        <div class="pet-heartbeat__status-main">
          <div class="pet-heartbeat__status-icon" aria-hidden="true">
            <Clock3 :size="25" :stroke-width="1.7" />
          </div>
          <div>
            <strong>{{ t(`pet.heartbeat.status.${statusTone}`) }}</strong>
            <span>{{ t('pet.heartbeat.statusDetail', { status: lastStatusLabel }) }}</span>
          </div>
        </div>

        <div class="pet-heartbeat__metrics">
          <div>
            <span>{{ t('pet.heartbeat.metrics.nextRun') }}</span>
            <strong>{{ nextRunLabel }}</strong>
          </div>
          <div>
            <span>{{ t('pet.heartbeat.metrics.interval') }}</span>
            <strong>{{ draft.intervalMinutes }} {{ t('pet.heartbeat.minutes') }}</strong>
          </div>
          <div>
            <span>{{ t('pet.heartbeat.metrics.lastFinished') }}</span>
            <strong>{{ lastFinishedLabel }}</strong>
          </div>
        </div>

        <div class="pet-heartbeat__actions">
          <button type="button" class="pet-heartbeat__button pet-heartbeat__button--primary" :disabled="!canRunNow" @click="void runNow()">
            <Play v-if="workingAction !== 'run'" :size="15" :stroke-width="2" aria-hidden="true" />
            <Activity v-else class="is-spinning" :size="15" aria-hidden="true" />
            {{ workingAction === 'run' ? t('pet.heartbeat.runningAction') : t('pet.heartbeat.runNow') }}
          </button>
          <button type="button" class="pet-heartbeat__button pet-heartbeat__button--danger" :disabled="!canCancel" @click="void cancel()">
            <Square v-if="workingAction !== 'cancel'" :size="14" :stroke-width="2" aria-hidden="true" />
            <Activity v-else class="is-spinning" :size="14" aria-hidden="true" />
            {{ workingAction === 'cancel' ? t('pet.heartbeat.cancelling') : t('pet.heartbeat.cancel') }}
          </button>
        </div>
      </section>

      <section class="pet-heartbeat__config-panel" aria-labelledby="pet-heartbeat-config-title">
        <div class="pet-heartbeat__panel-header">
          <div>
            <p class="pet-heartbeat__eyebrow">{{ t('pet.heartbeat.configKicker') }}</p>
            <h2 id="pet-heartbeat-config-title">{{ t('pet.heartbeat.configTitle') }}</h2>
          </div>
          <label class="pet-heartbeat__switch">
            <input v-model="draft.enabled" type="checkbox" :aria-label="t('pet.heartbeat.enabled')" />
            <span aria-hidden="true"></span>
          </label>
        </div>

        <div class="pet-heartbeat__field">
          <div class="pet-heartbeat__field-heading">
            <label for="pet-heartbeat-interval">{{ t('pet.heartbeat.interval') }}</label>
            <span>{{ t('pet.heartbeat.intervalRange') }}</span>
          </div>
          <div class="pet-heartbeat__number-control">
            <input id="pet-heartbeat-interval" v-model.number="draft.intervalMinutes" type="number" min="1" max="1440" step="1" :aria-invalid="Boolean(intervalError)" />
            <span>{{ t('pet.heartbeat.minutes') }}</span>
          </div>
          <div class="pet-heartbeat__presets" role="group" :aria-label="t('pet.heartbeat.presets')">
            <button v-for="preset in [15, 30, 60, 240]" :key="preset" type="button" :class="{ 'is-active': draft.intervalMinutes === preset }" @click="setIntervalPreset(preset)">
              {{ preset }}{{ t('pet.heartbeat.minutesShort') }}
            </button>
          </div>
          <p v-if="intervalError" class="pet-heartbeat__field-error">{{ intervalError }}</p>
        </div>

        <div class="pet-heartbeat__field pet-heartbeat__field--prompt">
          <div class="pet-heartbeat__field-heading">
            <label for="pet-heartbeat-prompt">{{ t('pet.heartbeat.prompt') }}</label>
            <span>{{ promptLength.toLocaleString(locale) }} / {{ PET_HEARTBEAT_MAX_PROMPT_LENGTH.toLocaleString(locale) }}</span>
          </div>
          <textarea id="pet-heartbeat-prompt" v-model="draft.prompt" rows="9" :maxlength="PET_HEARTBEAT_MAX_PROMPT_LENGTH" :placeholder="t('pet.heartbeat.promptPlaceholder')" :aria-invalid="Boolean(promptError)"></textarea>
          <p class="pet-heartbeat__hint">{{ t('pet.heartbeat.promptHint') }}</p>
          <p v-if="promptError" class="pet-heartbeat__field-error">{{ promptError }}</p>
        </div>

        <div class="pet-heartbeat__template-note">
          <span>{{ t('pet.heartbeat.templateLabel') }}</span>
          <code>{{ t('pet.heartbeat.templateTokens') }}</code>
        </div>

        <div v-if="errorMessage" class="pet-heartbeat__inline-error">{{ errorMessage }}</div>
        <div class="pet-heartbeat__save-row">
          <span v-if="isDirty" class="pet-heartbeat__unsaved">{{ t('pet.heartbeat.unsaved') }}</span>
          <button type="button" class="pet-heartbeat__button pet-heartbeat__button--primary" :disabled="saving || loading || !isDirty || Boolean(formError)" @click="void saveConfig()">
            <Save v-if="!saving" :size="15" :stroke-width="1.9" aria-hidden="true" />
            <Activity v-else class="is-spinning" :size="15" aria-hidden="true" />
            {{ saving ? t('pet.common.saving') : t('pet.heartbeat.save') }}
          </button>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.pet-heartbeat {
  --heartbeat-ink: var(--mac-text, #1d1d1f);
  --heartbeat-muted: var(--mac-text-secondary, #6e6e73);
  --heartbeat-line: var(--mac-border, rgba(15, 23, 42, 0.12));
  --heartbeat-surface: var(--mac-surface, #ffffff);
  --heartbeat-strong-surface: var(--mac-surface-strong, #f5f5f7);
  --heartbeat-accent: var(--mac-accent, #0a84ff);
  display: flex;
  width: 100%;
  min-height: 100%;
  flex-direction: column;
  gap: 24px;
  box-sizing: border-box;
  padding: 34px 48px 52px;
  color: var(--heartbeat-ink);
  font-family: var(--mac-font, system-ui, sans-serif);
}

/* 设置页已经提供外层留白；嵌入页签时去掉独立页面的内边距，避免内容被双重压缩。 */
.pet-heartbeat--embedded {
  padding: 0;
}

.pet-heartbeat h1,
.pet-heartbeat h2,
.pet-heartbeat p {
  margin: 0;
}

.pet-heartbeat__header,
.pet-heartbeat__heading,
.pet-heartbeat__panel-header,
.pet-heartbeat__status-main,
.pet-heartbeat__actions,
.pet-heartbeat__save-row,
.pet-heartbeat__field-heading,
.pet-heartbeat__template-note {
  display: flex;
  align-items: center;
}

.pet-heartbeat__header {
  justify-content: space-between;
  gap: 20px;
}

.pet-heartbeat__heading {
  min-width: 0;
  gap: 13px;
}

.pet-heartbeat__heading-icon,
.pet-heartbeat__status-icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 10px;
  background: color-mix(in srgb, var(--heartbeat-accent) 13%, transparent);
  color: var(--heartbeat-accent);
}

.pet-heartbeat__heading-icon {
  width: 42px;
  height: 42px;
}

.pet-heartbeat__eyebrow {
  color: var(--heartbeat-accent);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.14em;
  line-height: 1.3;
  text-transform: uppercase;
}

.pet-heartbeat h1 {
  margin-top: 4px;
  font-size: 24px;
  font-weight: 650;
  letter-spacing: 0;
  line-height: 1.2;
}

.pet-heartbeat__subtitle {
  margin-top: 5px !important;
  color: var(--heartbeat-muted);
  font-size: 12px;
  line-height: 1.5;
}

.pet-heartbeat__icon-button,
.pet-heartbeat__button,
.pet-heartbeat__presets button {
  box-sizing: border-box;
  border: 1px solid var(--heartbeat-line);
  margin: 0;
  font: inherit;
  cursor: pointer;
}

.pet-heartbeat__icon-button {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: transparent;
  color: var(--heartbeat-muted);
}

.pet-heartbeat__icon-button:hover:not(:disabled) {
  background: var(--heartbeat-strong-surface);
  color: var(--heartbeat-ink);
}

.pet-heartbeat__layout {
  display: grid;
  grid-template-columns: minmax(0, 0.92fr) minmax(0, 1.08fr);
  align-items: start;
  gap: 18px;
}

.pet-heartbeat__status-panel,
.pet-heartbeat__config-panel {
  min-width: 0;
  border: 1px solid var(--heartbeat-line);
  border-radius: 12px;
  background: color-mix(in srgb, var(--heartbeat-surface) 88%, transparent);
  box-shadow: 0 12px 32px color-mix(in srgb, #26364a 8%, transparent);
}

.pet-heartbeat__status-panel {
  display: flex;
  min-height: 372px;
  flex-direction: column;
  gap: 26px;
  padding: 22px;
}

.pet-heartbeat__config-panel {
  display: flex;
  flex-direction: column;
  gap: 22px;
  padding: 22px;
}

.pet-heartbeat__panel-header {
  justify-content: space-between;
  gap: 16px;
}

.pet-heartbeat h2 {
  margin-top: 4px;
  font-size: 17px;
  font-weight: 650;
  line-height: 1.3;
}

.pet-heartbeat__phase {
  display: inline-flex;
  max-width: 46%;
  min-width: 0;
  align-items: center;
  gap: 6px;
  overflow: hidden;
  color: var(--heartbeat-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-heartbeat__phase-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 50%;
  background: #99a2ad;
}

[data-tone='running'] .pet-heartbeat__phase-dot {
  background: #dc8b34;
  box-shadow: 0 0 0 4px color-mix(in srgb, #dc8b34 13%, transparent);
}

[data-tone='waiting'] .pet-heartbeat__phase-dot,
[data-tone='scheduled'] .pet-heartbeat__phase-dot {
  background: #328c5d;
}

.pet-heartbeat__status-main {
  gap: 12px;
}

.pet-heartbeat__status-icon {
  width: 52px;
  height: 52px;
  border-radius: 14px;
}

.pet-heartbeat__status-main strong,
.pet-heartbeat__status-main span {
  display: block;
}

.pet-heartbeat__status-main strong {
  font-size: 16px;
  line-height: 1.3;
}

.pet-heartbeat__status-main span {
  margin-top: 4px;
  color: var(--heartbeat-muted);
  font-size: 11px;
  line-height: 1.45;
}

.pet-heartbeat__metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 1px;
  overflow: hidden;
  border: 1px solid var(--heartbeat-line);
  border-radius: 9px;
  background: var(--heartbeat-line);
}

.pet-heartbeat__metrics > div {
  display: flex;
  min-width: 0;
  min-height: 78px;
  flex-direction: column;
  gap: 7px;
  padding: 12px 10px;
  background: color-mix(in srgb, var(--heartbeat-strong-surface) 58%, var(--heartbeat-surface));
}

.pet-heartbeat__metrics span {
  overflow: hidden;
  color: var(--heartbeat-muted);
  font-size: 10px;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-heartbeat__metrics strong {
  overflow: hidden;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-heartbeat__actions {
  flex-wrap: wrap;
  gap: 8px;
  margin-top: auto;
}

.pet-heartbeat__button {
  display: inline-flex;
  width: auto;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  border-radius: 8px;
  padding: 7px 11px;
  background: transparent;
  color: var(--heartbeat-ink);
  font-size: 11px;
  line-height: 1.25;
  white-space: nowrap;
}

.pet-heartbeat__button--primary {
  border-color: color-mix(in srgb, var(--heartbeat-accent) 45%, var(--heartbeat-line));
  background: color-mix(in srgb, var(--heartbeat-accent) 11%, transparent);
  color: var(--heartbeat-accent);
}

.pet-heartbeat__button--quiet {
  border-color: color-mix(in srgb, var(--heartbeat-accent) 40%, var(--heartbeat-line));
  color: var(--heartbeat-accent);
}

.pet-heartbeat__button--danger {
  border-color: color-mix(in srgb, #bd4f4f 42%, var(--heartbeat-line));
  color: #bd4f4f;
}

.pet-heartbeat__button:hover:not(:disabled) {
  filter: brightness(0.96);
}

.pet-heartbeat__button:disabled,
.pet-heartbeat__icon-button:disabled,
.pet-heartbeat__presets button:disabled {
  cursor: wait;
  opacity: 0.45;
}

.pet-heartbeat__switch {
  position: relative;
  display: inline-flex;
  width: 40px;
  height: 22px;
  flex: 0 0 auto;
  cursor: pointer;
}

.pet-heartbeat__switch input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.pet-heartbeat__switch span {
  position: relative;
  display: block;
  width: 100%;
  height: 100%;
  border-radius: 999px;
  background: color-mix(in srgb, var(--heartbeat-muted) 30%, transparent);
  transition: background 0.18s ease;
}

.pet-heartbeat__switch span::after {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
  content: '';
  transition: transform 0.18s ease;
}

.pet-heartbeat__switch input:checked + span {
  background: var(--heartbeat-accent);
}

.pet-heartbeat__switch input:checked + span::after {
  transform: translateX(18px);
}

.pet-heartbeat__field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 8px;
}

.pet-heartbeat__field-heading {
  justify-content: space-between;
  gap: 12px;
}

.pet-heartbeat__field-heading label {
  color: var(--heartbeat-ink);
  font-size: 12px;
  font-weight: 600;
}

.pet-heartbeat__field-heading span,
.pet-heartbeat__hint,
.pet-heartbeat__unsaved {
  color: var(--heartbeat-muted);
  font-size: 10px;
  line-height: 1.45;
}

.pet-heartbeat__number-control {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pet-heartbeat__number-control input,
.pet-heartbeat__field textarea {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--heartbeat-line);
  border-radius: 8px;
  background: color-mix(in srgb, var(--heartbeat-strong-surface) 72%, transparent);
  color: var(--heartbeat-ink);
  font: inherit;
  font-size: 12px;
  outline: none;
  transition: border-color 0.18s ease, box-shadow 0.18s ease, background 0.18s ease;
}

.pet-heartbeat__number-control input {
  max-width: 150px;
  padding: 8px 9px;
  font-variant-numeric: tabular-nums;
}

.pet-heartbeat__number-control > span {
  color: var(--heartbeat-muted);
  font-size: 11px;
}

.pet-heartbeat__field textarea {
  min-height: 156px;
  resize: vertical;
  padding: 9px 10px;
  line-height: 1.55;
}

.pet-heartbeat__number-control input:focus,
.pet-heartbeat__field textarea:focus {
  border-color: var(--heartbeat-accent);
  background: var(--heartbeat-surface);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--heartbeat-accent) 16%, transparent);
}

.pet-heartbeat__number-control input[aria-invalid='true'],
.pet-heartbeat__field textarea[aria-invalid='true'] {
  border-color: #bd4f4f;
}

.pet-heartbeat__presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.pet-heartbeat__presets button {
  min-width: 0;
  border-radius: 7px;
  padding: 5px 9px;
  background: transparent;
  color: var(--heartbeat-muted);
  font-size: 10px;
  line-height: 1.25;
}

.pet-heartbeat__presets button:hover,
.pet-heartbeat__presets button.is-active {
  border-color: color-mix(in srgb, var(--heartbeat-accent) 45%, var(--heartbeat-line));
  background: color-mix(in srgb, var(--heartbeat-accent) 10%, transparent);
  color: var(--heartbeat-accent);
}

.pet-heartbeat__field-error,
.pet-heartbeat__inline-error {
  color: #bd4f4f;
  font-size: 11px;
  line-height: 1.45;
}

.pet-heartbeat__hint {
  margin-top: -1px !important;
}

.pet-heartbeat__template-note {
  min-width: 0;
  align-items: flex-start;
  flex-direction: column;
  gap: 6px;
  border-top: 1px solid color-mix(in srgb, var(--heartbeat-line) 72%, transparent);
  padding-top: 14px;
}

.pet-heartbeat__template-note span {
  color: var(--heartbeat-muted);
  font-size: 10px;
}

.pet-heartbeat__template-note code {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: var(--heartbeat-accent);
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 11px;
  line-height: 1.45;
}

.pet-heartbeat__save-row {
  justify-content: flex-end;
  gap: 12px;
  border-top: 1px solid color-mix(in srgb, var(--heartbeat-line) 72%, transparent);
  padding-top: 16px;
}

.pet-heartbeat__state {
  display: flex;
  min-height: 180px;
  align-items: center;
  justify-content: center;
  gap: 9px;
  border: 1px dashed var(--heartbeat-line);
  border-radius: 12px;
  color: var(--heartbeat-muted);
  font-size: 12px;
}

.pet-heartbeat__state.is-error {
  flex-wrap: wrap;
  color: #bd4f4f;
}

.is-spinning {
  animation: pet-heartbeat-spin 0.9s linear infinite;
}

@keyframes pet-heartbeat-spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 900px) {
  .pet-heartbeat__layout {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 640px) {
  .pet-heartbeat {
    gap: 18px;
    padding: 22px 14px 34px;
  }

  .pet-heartbeat__header {
    align-items: flex-start;
  }

  .pet-heartbeat__subtitle {
    max-width: 32em;
  }

  .pet-heartbeat__status-panel,
  .pet-heartbeat__config-panel {
    padding: 17px;
  }

  .pet-heartbeat__metrics {
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-heartbeat__metrics > div {
    min-height: 0;
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .pet-heartbeat__metrics strong {
    text-align: right;
  }

  .pet-heartbeat__actions .pet-heartbeat__button {
    flex: 1 1 140px;
  }
}
</style>
