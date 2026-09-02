import { Call } from '../../wails-runtime-compat'
import {
  DEFAULT_PET_ID,
  type PetHeartbeatConfig,
  type PetHeartbeatEvent,
  type PetHeartbeatPhase,
  type PetHeartbeatRunStatus,
  type PetHeartbeatSnapshot
} from './petTypes'

const PET_HEARTBEAT_SERVICE = 'codeswitch/services.PetHeartbeatAPI'

export const PET_HEARTBEAT_METHODS = {
  getSnapshot: `${PET_HEARTBEAT_SERVICE}.GetSnapshot`,
  saveConfig: `${PET_HEARTBEAT_SERVICE}.SaveConfig`,
  runNow: `${PET_HEARTBEAT_SERVICE}.RunNow`,
  cancel: `${PET_HEARTBEAT_SERVICE}.Cancel`
} as const

export interface PetHeartbeatRuntimeAdapter {
  call(method: string, ...args: unknown[]): Promise<unknown>
}

export interface PetHeartbeatApi {
  getSnapshot(): Promise<PetHeartbeatSnapshot>
  saveConfig(config: PetHeartbeatConfig): Promise<PetHeartbeatSnapshot>
  runNow(): Promise<PetHeartbeatSnapshot>
  cancel(): Promise<PetHeartbeatSnapshot>
}

const wailsAdapter: PetHeartbeatRuntimeAdapter = {
  call(method, ...args) {
    return Promise.resolve(Call.ByName(method, ...args))
  }
}

const phases: readonly PetHeartbeatPhase[] = ['disabled', 'waiting', 'waiting_for_idle', 'running']
const statuses: readonly PetHeartbeatRunStatus[] = ['none', 'completed', 'failed', 'cancelled', 'interrupted']

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function finiteNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function nonNegativeNumber(value: unknown): number {
  return Math.max(0, finiteNumber(value))
}

function normalizePhase(value: unknown): PetHeartbeatPhase {
  return typeof value === 'string' && phases.includes(value as PetHeartbeatPhase)
    ? value as PetHeartbeatPhase
    : 'disabled'
}

function normalizeStatus(value: unknown): PetHeartbeatRunStatus {
  return typeof value === 'string' && statuses.includes(value as PetHeartbeatRunStatus)
    ? value as PetHeartbeatRunStatus
    : 'none'
}

function asRecord(value: unknown): Record<string, unknown> {
  return isRecord(value) ? value : {}
}

/**
 * Wails 和浏览器 bridge 都可能返回缺省字段；页面只消费归一化结果，避免坏事件
 * 把倒计时或运行按钮推入一个不可解释的状态。
 */
export function normalizePetHeartbeatSnapshot(value: unknown): PetHeartbeatSnapshot {
  const root = asRecord(value)
  const configRoot = asRecord(root.config)
  const runtimeRoot = asRecord(root.runtime)
  const enabled = configRoot.enabled === true
  const intervalMinutes = Math.floor(finiteNumber(configRoot.intervalMinutes, 30))
  return {
    config: {
      petId: typeof configRoot.petId === 'string' && configRoot.petId.trim()
        ? configRoot.petId.trim()
        : DEFAULT_PET_ID,
      enabled,
      intervalMinutes: Math.min(1440, Math.max(1, intervalMinutes)),
      prompt: typeof configRoot.prompt === 'string' ? configRoot.prompt : ''
    },
    runtime: {
      phase: normalizePhase(runtimeRoot.phase),
      nextRunAt: nonNegativeNumber(runtimeRoot.nextRunAt),
      currentRequestId: typeof runtimeRoot.currentRequestId === 'string' ? runtimeRoot.currentRequestId : '',
      lastStartedAt: nonNegativeNumber(runtimeRoot.lastStartedAt),
      lastFinishedAt: nonNegativeNumber(runtimeRoot.lastFinishedAt),
      lastStatus: normalizeStatus(runtimeRoot.lastStatus),
      lastErrorCode: typeof runtimeRoot.lastErrorCode === 'string' ? runtimeRoot.lastErrorCode : ''
    }
  }
}

export function normalizePetHeartbeatEvent(value: unknown): PetHeartbeatEvent | null {
  const root = asRecord(value)
  const rawSnapshot = root.snapshot ?? value
  if (!isRecord(rawSnapshot) || !isRecord(asRecord(rawSnapshot).config)) return null
  return {
    type: typeof root.type === 'string' ? root.type : 'state_changed',
    snapshot: normalizePetHeartbeatSnapshot(rawSnapshot)
  }
}

export function createPetHeartbeatApi(adapter: PetHeartbeatRuntimeAdapter = wailsAdapter): PetHeartbeatApi {
  async function getSnapshot(): Promise<PetHeartbeatSnapshot> {
    return normalizePetHeartbeatSnapshot(await adapter.call(PET_HEARTBEAT_METHODS.getSnapshot))
  }

  async function saveConfig(config: PetHeartbeatConfig): Promise<PetHeartbeatSnapshot> {
    const payload: PetHeartbeatConfig = {
      petId: config.petId?.trim() || DEFAULT_PET_ID,
      enabled: config.enabled === true,
      intervalMinutes: Math.floor(config.intervalMinutes),
      prompt: config.prompt.trim()
    }
    return normalizePetHeartbeatSnapshot(await adapter.call(PET_HEARTBEAT_METHODS.saveConfig, payload))
  }

  async function runNow(): Promise<PetHeartbeatSnapshot> {
    return normalizePetHeartbeatSnapshot(await adapter.call(PET_HEARTBEAT_METHODS.runNow))
  }

  async function cancel(): Promise<PetHeartbeatSnapshot> {
    return normalizePetHeartbeatSnapshot(await adapter.call(PET_HEARTBEAT_METHODS.cancel))
  }

  return { getSnapshot, saveConfig, runNow, cancel }
}

export const petHeartbeatApi = createPetHeartbeatApi()
