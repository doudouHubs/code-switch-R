import { Call } from '../wails-runtime-compat'

const SERVICE = 'codeswitch/services/channels.ChannelService'

export interface ChannelConfigField {
  key: string
  label: string
  secret?: boolean
  required?: boolean
  placeholder?: string
}

export interface ChannelDescriptor {
  type: string
  displayName: string
  description: string
  icon: string
  builtin: boolean
  tools: string[]
  configSchema: ChannelConfigField[]
}

export interface ChannelFeatures {
  autoReply: boolean
  streamingReply: boolean
  autoStart: boolean
}

export interface ChannelPermissions {
  allowReadHome: boolean
  readablePathPrefixes: string[]
  allowWriteOutside: boolean
  allowShell: boolean
  allowSubAgents: boolean
}

export interface ChannelInstance {
  id: string
  type: string
  name: string
  enabled: boolean
  builtin: boolean
  config: Record<string, string>
  createdAt: number
  projectId?: string | null
  providerPlatform?: string
  providerId?: string | null
  model?: string | null
  tools: Record<string, boolean>
  features: ChannelFeatures
  permissions: ChannelPermissions
  status: string
  lastError?: string
  updatedAt: number
}

export interface ProjectBinding {
  id: string
  path: string
  name: string
}

export interface ChannelSession {
  id: string
  instanceId: string
  chatId: string
  chatName?: string
  senderId?: string
  senderName?: string
  projectId: string
  workingFolder: string
  createdAt: number
  updatedAt: number
}

export interface ChannelMedia {
  id?: string
  kind: string
  mediaType: string
  fileName?: string
  data?: number[]
}

export interface ChannelMessage {
  id: string
  instanceId: string
  sessionId?: string
  externalId?: string
  role: string
  chatId: string
  senderId?: string
  senderName?: string
  content: string
  images?: ChannelMedia[]
  audio?: ChannelMedia
  timestamp: number
}

export interface ChannelStatus {
  instanceId: string
  state: string
  error?: string
  updatedAt: number
}

export interface WeixinLoginStartResult {
  sessionKey: string
  qrcode?: string
  qrDataUrl?: string
  qrUrl?: string
  status: string
  message: string
}

export interface WeixinLoginWaitResult {
  sessionKey?: string
  status: string
  connected: boolean
  message: string
  qrcode?: string
  qrDataUrl?: string
  qrUrl?: string
  token?: string
  accountId?: string
  baseUrl?: string
  userId?: string
}

export function listChannelDescriptors(): Promise<ChannelDescriptor[]> {
  return Call.ByName(`${SERVICE}.ListDescriptors`) as Promise<ChannelDescriptor[]>
}

export function listChannelInstances(): Promise<ChannelInstance[]> {
  return Call.ByName(`${SERVICE}.ListInstances`) as Promise<ChannelInstance[]>
}

export function listChannelProjects(): Promise<ProjectBinding[]> {
  return Call.ByName(`${SERVICE}.ListProjects`) as Promise<ProjectBinding[]>
}

export function saveChannelInstance(instance: ChannelInstance): Promise<void> {
  return Call.ByName(`${SERVICE}.SaveInstance`, instance) as Promise<void>
}

export function removeChannelInstance(id: string): Promise<void> {
  return Call.ByName(`${SERVICE}.RemoveInstance`, id) as Promise<void>
}

export function setChannelEnabled(id: string, enabled: boolean): Promise<void> {
  return Call.ByName(`${SERVICE}.SetEnabled`, id, enabled) as Promise<void>
}

export function startChannel(id: string): Promise<void> {
  return Call.ByName(`${SERVICE}.Start`, id) as Promise<void>
}

export function stopChannel(id: string): Promise<void> {
  return Call.ByName(`${SERVICE}.Stop`, id) as Promise<void>
}

export function getChannelStatus(id: string): Promise<ChannelStatus> {
  return Call.ByName(`${SERVICE}.GetStatus`, id) as Promise<ChannelStatus>
}

export function listChannelSessions(instanceId: string): Promise<ChannelSession[]> {
  return Call.ByName(`${SERVICE}.ListSessions`, instanceId) as Promise<ChannelSession[]>
}

export function listChannelMessages(sessionId: string, limit = 200): Promise<ChannelMessage[]> {
  return Call.ByName(`${SERVICE}.ListMessages`, sessionId, limit) as Promise<ChannelMessage[]>
}

export function sendChannelMessage(instanceId: string, chatId: string, content: string): Promise<string> {
  return Call.ByName(`${SERVICE}.SendMessage`, instanceId, chatId, content) as Promise<string>
}

export function startWeixinLogin(instanceId: string): Promise<WeixinLoginStartResult> {
  return Call.ByName(`${SERVICE}.StartWeixinLogin`, instanceId) as Promise<WeixinLoginStartResult>
}

export function waitWeixinLogin(instanceId: string, sessionKey: string): Promise<WeixinLoginWaitResult> {
  return Call.ByName(`${SERVICE}.WaitWeixinLogin`, instanceId, sessionKey) as Promise<WeixinLoginWaitResult>
}

export function cancelWeixinLogin(instanceId: string, sessionKey: string): Promise<void> {
  return Call.ByName(`${SERVICE}.CancelWeixinLogin`, instanceId, sessionKey) as Promise<void>
}
