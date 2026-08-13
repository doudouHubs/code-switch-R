import { Call } from '@wailsio/runtime'

export type RadarPoint = {
  model: string
  effort: string
  iq: number
  passed: number
  valid_tasks: number
  average_price_usd: number | null
  price_samples: number
  average_minutes: number | null
  duration_samples: number
  combined_cost_index: number | null
}

export type RadarSnapshot = {
  source_updated_at: string
  fetched_at: string
  points: RadarPoint[]
}

// 前端只消费后端聚合后的快照，避免复制原站任务明细解析逻辑造成口径漂移。
export const fetchRadarSnapshot = async (): Promise<RadarSnapshot> => {
  return Call.ByName('codeswitch/services.RadarService.GetSnapshot') as Promise<RadarSnapshot>
}
