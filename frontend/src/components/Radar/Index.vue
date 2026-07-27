<template>
  <section class="radar-page" aria-label="Codex雷达">
    <header class="radar-head">
      <div class="radar-title-row">
        <h1><span aria-hidden="true">&#9889;</span> 智力效率</h1>
        <p :class="['radar-status', { 'is-stale': isStale, 'is-error': !snapshot && loadError }]">
          {{ statusText }}
        </p>
        <button
          class="ghost-icon radar-refresh-button"
          :class="{ 'is-refreshing': loading }"
          :disabled="loading"
          :title="loading ? '正在刷新' : '立即刷新'"
          :aria-label="loading ? '正在刷新' : '立即刷新'"
          @click="refreshRadar"
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M20 11a8 8 0 10.3 3.1" fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="2" />
            <path d="M20 4v7h-7" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" />
          </svg>
        </button>
      </div>
      <p class="radar-callout">参与的人越多，数据越准。欢迎前往分布式雷达贡献一份力量。</p>
    </header>

    <div v-if="loading && !snapshot" class="radar-state radar-loading" role="status">
      正在读取 Codex 雷达数据...
    </div>

    <div v-else-if="!snapshot" class="radar-state radar-error" role="alert">
      <strong>未能连接 Codex 雷达</strong>
      <span>{{ loadError }}</span>
      <button class="radar-retry-button" type="button" @click="refreshRadar">重新尝试</button>
    </div>

    <template v-else>
      <section class="radar-card-groups" aria-label="模型智力效率">
        <div
          v-for="group in modelGroups"
          :key="group.model"
          class="radar-card-group"
          :class="`radar-card-group--${group.tone}`"
        >
          <article
            v-for="point in group.points"
            :key="`${point.model}-${point.effort}`"
            class="radar-card"
            :style="{ '--radar-accent': group.color, '--radar-accent-soft': group.softColor }"
            :title="`${modelLabel(point.model)} ${point.effort}: ${point.passed}/${point.valid_tasks}`"
          >
            <div class="radar-card-main">
              <div class="radar-card-heading">
                <span class="radar-card-model">{{ modelLabel(point.model) }}</span>
                <span class="radar-card-effort">{{ point.effort }}</span>
              </div>
              <strong class="radar-card-iq">{{ formatIQ(point.iq) }}</strong>
            </div>
            <div class="radar-card-metrics">
              <strong>{{ formatCost(point.average_price_usd) }}</strong>
              <span>{{ formatMinutes(point.average_minutes) }}</span>
            </div>
          </article>
        </div>
      </section>

      <section class="radar-chart-panel" aria-label="综合成本乘以智力图表">
        <header class="radar-chart-head">
          <div class="radar-chart-title">
            <h2>综合成本 × IQ</h2>
            <span>切换指标</span>
            <select aria-label="当前图表指标" disabled>
              <option>综合成本 × IQ</option>
            </select>
            <time v-if="snapshot.source_updated_at" :datetime="snapshot.source_updated_at">
              {{ formatTimestamp(snapshot.source_updated_at) }} 更新
            </time>
          </div>
          <div class="radar-legend" aria-label="模型图例">
            <span v-for="group in chartGroups" :key="group.model" :style="{ '--radar-legend-color': group.color }">
              {{ group.label }}
            </span>
          </div>
        </header>
        <div class="radar-chart-canvas">
          <Line v-if="chartPoints.length" :data="chartData" :options="chartOptions" />
          <p v-else class="radar-chart-empty">当前没有可绘制的综合成本数据。</p>
        </div>
      </section>

      <p class="radar-attribution">
        数据来自
        <a href="https://codexradar.com/" target="_blank" rel="noreferrer">Codex 雷达 codexradar.com</a>
      </p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onUnmounted, ref } from 'vue'
import {
  Chart,
  Legend,
  LineElement,
  LinearScale,
  LogarithmicScale,
  PointElement,
  Tooltip,
  type ChartData,
  type ChartOptions,
  type Plugin,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import { fetchRadarSnapshot, type RadarPoint, type RadarSnapshot } from '../../services/radar'

type RadarModelMeta = {
  model: string
  label: string
  tone: string
  color: string
  softColor: string
}

type RadarModelGroup = RadarModelMeta & {
  points: RadarPoint[]
}

type RadarChartPoint = {
  x: number
  y: number
  effort: string
  point: RadarPoint
}

const REFRESH_INTERVAL_MS = 10 * 60 * 1000
const EFFORT_ORDER = ['ultra', 'max', 'xhigh', 'high', 'medium', 'low']
const MODEL_META: RadarModelMeta[] = [
  { model: 'gpt-5.6-sol', label: 'Sol', tone: 'sol', color: '#f7c600', softColor: 'rgba(247, 198, 0, 0.16)' },
  { model: 'gpt-5.6-terra', label: 'Terra', tone: 'terra', color: '#3d82ff', softColor: 'rgba(61, 130, 255, 0.16)' },
  { model: 'gpt-5.6-luna', label: 'Luna', tone: 'luna', color: '#d7dfec', softColor: 'rgba(215, 223, 236, 0.14)' },
  { model: 'gpt-5.5', label: 'GPT-5.5', tone: 'gpt55', color: '#0bd8e9', softColor: 'rgba(11, 216, 233, 0.15)' },
]

const modelMetaByID = new Map(MODEL_META.map((item) => [item.model, item]))
const snapshot = ref<RadarSnapshot | null>(null)
const loading = ref(false)
const loadError = ref('')
const isStale = ref(false)
let refreshTimer: number | undefined

const modelLabel = (model: string) => modelMetaByID.get(model)?.label ?? model.replace(/^gpt-/i, 'GPT-')

const sortByEffort = (left: RadarPoint, right: RadarPoint) => {
  const leftIndex = EFFORT_ORDER.indexOf(left.effort)
  const rightIndex = EFFORT_ORDER.indexOf(right.effort)
  return (leftIndex === -1 ? Number.MAX_SAFE_INTEGER : leftIndex) - (rightIndex === -1 ? Number.MAX_SAFE_INTEGER : rightIndex)
}

const modelGroups = computed<RadarModelGroup[]>(() => {
  const pointGroups = new Map<string, RadarPoint[]>()
  for (const point of snapshot.value?.points ?? []) {
    const current = pointGroups.get(point.model) ?? []
    current.push(point)
    pointGroups.set(point.model, current)
  }

  const knownGroups = MODEL_META.map((meta) => ({
    ...meta,
    points: [...(pointGroups.get(meta.model) ?? [])].sort(sortByEffort),
  })).filter((group) => group.points.length)

  // 保留未来新增模型，避免数据源新增档位后页面静默丢失整组信息。
  const unknownGroups = [...pointGroups.entries()]
    .filter(([model]) => !modelMetaByID.has(model))
    .map(([model, points]) => ({
      model,
      label: modelLabel(model),
      tone: 'unknown',
      color: '#a8b4c8',
      softColor: 'rgba(168, 180, 200, 0.14)',
      points: [...points].sort(sortByEffort),
    }))

  return [...knownGroups, ...unknownGroups]
})

const chartGroups = computed(() => modelGroups.value.filter((group) => group.points.some((point) => point.combined_cost_index && point.combined_cost_index > 0)))
const chartPoints = computed(() => chartGroups.value.flatMap((group) => group.points))

const chartData = computed<ChartData<'line', RadarChartPoint[]>>(() => ({
  datasets: chartGroups.value.map((group) => ({
    label: group.label,
    data: group.points
      .filter((point): point is RadarPoint & { combined_cost_index: number } => Number.isFinite(point.combined_cost_index) && (point.combined_cost_index ?? 0) > 0)
      .sort((left, right) => (left.combined_cost_index ?? 0) - (right.combined_cost_index ?? 0))
      .map((point) => ({ x: point.combined_cost_index, y: point.iq, effort: point.effort, point })),
    parsing: false,
    borderColor: group.color,
    backgroundColor: group.color,
    pointBackgroundColor: '#0b1422',
    pointBorderColor: group.color,
    pointBorderWidth: 2,
    pointRadius: 5,
    pointHoverRadius: 7,
    borderWidth: 2,
    tension: 0.22,
    fill: false,
  })),
}))

const radarPointLabelPlugin: Plugin<'line'> = {
  id: 'radarPointLabel',
  afterDatasetsDraw(chart) {
    const { ctx, chartArea } = chart
    ctx.save()
    ctx.fillStyle = '#a8b4c8'
    ctx.font = '600 10px "Segoe UI Variable", "Microsoft YaHei", sans-serif'
    ctx.textAlign = 'center'

    chart.data.datasets.forEach((dataset, datasetIndex) => {
      const meta = chart.getDatasetMeta(datasetIndex)
      meta.data.forEach((element, pointIndex) => {
        const point = dataset.data[pointIndex] as unknown as RadarChartPoint
        if (!point?.effort) return
        const marker = element as { x: number; y: number }
        const y = marker.y <= chartArea.top + 18 ? marker.y + 16 : marker.y - 9
        ctx.fillText(point.effort, marker.x, y)
      })
    })
    ctx.restore()
  },
}

Chart.register(LogarithmicScale, LinearScale, PointElement, LineElement, Tooltip, Legend, radarPointLabelPlugin)

const chartOptions: ChartOptions<'line'> = {
  responsive: true,
  maintainAspectRatio: false,
  layout: { padding: { top: 18, right: 18, bottom: 8, left: 6 } },
  interaction: { mode: 'nearest', intersect: true },
  plugins: {
    legend: { display: false },
    tooltip: {
      displayColors: false,
      backgroundColor: 'rgba(7, 15, 27, 0.96)',
      borderColor: 'rgba(136, 157, 192, 0.42)',
      borderWidth: 1,
      titleColor: '#f6f8fc',
      bodyColor: '#c8d4e7',
      callbacks: {
        title: (items) => {
          const raw = items[0]?.raw as RadarChartPoint | undefined
          return raw ? `${modelLabel(raw.point.model)} ${raw.effort}` : ''
        },
        label: (item) => {
          const raw = item.raw as RadarChartPoint
          return `IQ ${formatIQ(raw.y)} (${raw.point.passed}/${raw.point.valid_tasks})`
        },
        afterLabel: (item) => {
          const raw = item.raw as RadarChartPoint
          return [
            `综合成本 ${formatChartCost(raw.x)}`,
            `成本 ${formatCost(raw.point.average_price_usd)} · 耗时 ${formatMinutes(raw.point.average_minutes)}`,
          ]
        },
      },
    },
  },
  scales: {
    x: {
      type: 'logarithmic',
      title: { display: true, text: '综合成本指数', color: '#91a1bb', font: { size: 11, weight: 600 } },
      grid: { color: 'rgba(113, 135, 171, 0.18)' },
      border: { color: 'rgba(135, 157, 194, 0.55)' },
      ticks: {
        color: '#9babc4',
        font: { size: 10 },
        callback: (value) => formatChartCost(Number(value)),
      },
    },
    y: {
      beginAtZero: true,
      suggestedMax: 120,
      max: 150,
      title: { display: true, text: 'IQ', color: '#91a1bb', font: { size: 11, weight: 600 } },
      grid: { color: 'rgba(113, 135, 171, 0.18)' },
      border: { color: 'rgba(135, 157, 194, 0.55)' },
      ticks: { color: '#9babc4', font: { size: 10 }, stepSize: 20 },
    },
  },
}

const statusText = computed(() => {
  if (loading.value && !snapshot.value) return '正在读取数据'
  if (isStale.value) return '数据已过期'
  if (snapshot.value?.source_updated_at) return `${formatTimestamp(snapshot.value.source_updated_at)} 更新`
  if (snapshot.value?.fetched_at) return `${formatTimestamp(snapshot.value.fetched_at)} 更新`
  return '等待数据'
})

function formatTimestamp(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间未知'
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function formatIQ(value: number): string {
  return Number.isFinite(value) ? value.toFixed(1) : '--'
}

function formatCost(value: number | null): string {
  if (!Number.isFinite(value)) return '--'
  const amount = value as number
  if (amount >= 10) return `$${amount.toFixed(1)}`
  if (amount >= 1) return `$${amount.toFixed(1)}`
  return `$${amount.toFixed(2)}`
}

function formatMinutes(value: number | null): string {
  if (!Number.isFinite(value)) return '--'
  return `${Math.round(value as number)}分钟`
}

function formatChartCost(value: number): string {
  if (!Number.isFinite(value)) return '--'
  if (value >= 10) return Math.round(value).toString()
  if (value >= 1) return value.toFixed(1)
  if (value >= 0.01) return value.toFixed(2)
  return value.toFixed(4)
}

async function refreshRadar() {
  if (loading.value) return

  loading.value = true
  loadError.value = ''
  try {
    snapshot.value = await fetchRadarSnapshot()
    isStale.value = false
  } catch (error) {
    // 用户选择保留当前会话的最近成功快照；只有冷启动失败才进入空错误态。
    isStale.value = snapshot.value !== null
    loadError.value = error instanceof Error ? error.message : '请检查网络后重试。'
    console.error('failed to refresh Codex radar', error)
  } finally {
    loading.value = false
  }
}

function startPolling() {
  stopPolling()
  refreshTimer = window.setInterval(() => {
    void refreshRadar()
  }, REFRESH_INTERVAL_MS)
}

function stopPolling() {
  if (refreshTimer !== undefined) {
    window.clearInterval(refreshTimer)
    refreshTimer = undefined
  }
}

onActivated(() => {
  if (!snapshot.value) {
    void refreshRadar()
  }
  startPolling()
})

onDeactivated(stopPolling)
onUnmounted(stopPolling)
</script>

<style scoped>
.radar-page {
  --radar-bg: #08121f;
  --radar-panel: #0d1929;
  --radar-line: #2a3a54;
  --radar-text: #eef4ff;
  --radar-muted: #91a1bb;
  min-height: 100%;
  padding: 28px 36px 48px;
  background:
    linear-gradient(rgba(99, 136, 194, 0.035) 1px, transparent 1px),
    linear-gradient(90deg, rgba(99, 136, 194, 0.035) 1px, transparent 1px),
    var(--radar-bg);
  background-size: 28px 28px, 28px 28px, auto;
  color: var(--radar-text);
  font-family: "Segoe UI Variable", "Aptos", "Microsoft YaHei", sans-serif;
}

.radar-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 18px;
}

.radar-title-row,
.radar-chart-title,
.radar-legend {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.radar-title-row h1 {
  margin: 0;
  color: var(--radar-text);
  font-size: 24px;
  font-weight: 800;
  letter-spacing: 0;
  line-height: 1.2;
}

.radar-title-row h1 span {
  color: #f7c600;
}

.radar-status {
  margin: 0;
  color: #c4d2e7;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  font-weight: 650;
}

.radar-status.is-stale,
.radar-status.is-error {
  color: #f7c600;
}

.radar-refresh-button {
  width: 28px;
  height: 28px;
  min-width: 28px;
  padding: 0;
  border: 1px solid #334967;
  border-radius: 6px;
  background: #102035;
  color: #a8c4ec;
}

.radar-refresh-button:hover:not(:disabled) {
  background: #173252;
  color: #ffffff;
}

.radar-refresh-button.is-refreshing svg {
  animation: radar-spin 0.9s linear infinite;
}

.radar-callout {
  max-width: 430px;
  margin: 3px 0 0;
  color: #aebed3;
  font-size: 12px;
  font-weight: 600;
  line-height: 1.45;
  text-align: right;
}

.radar-card-groups {
  display: grid;
  gap: 10px;
}

.radar-card-group {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 10px;
}

.radar-card {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 68px;
  min-height: 78px;
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--radar-accent) 60%, #32455f);
  border-top: 3px solid var(--radar-accent);
  border-radius: 8px;
  background: linear-gradient(135deg, color-mix(in srgb, var(--radar-accent-soft) 58%, #0d1929), #0d1929 70%);
  box-shadow: 0 10px 22px rgba(0, 0, 0, 0.17);
}

.radar-card-main {
  display: grid;
  align-content: space-between;
  gap: 4px;
  min-width: 0;
  padding: 8px 10px 8px;
}

.radar-card-heading {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}

.radar-card-model,
.radar-card-effort {
  color: #dce8fb;
  font-size: 11px;
  font-weight: 760;
  line-height: 1;
  white-space: nowrap;
}

.radar-card-model {
  overflow: hidden;
  text-overflow: ellipsis;
}

.radar-card-iq {
  color: var(--radar-accent);
  font-size: 34px;
  font-variant-numeric: tabular-nums;
  font-weight: 800;
  letter-spacing: 0;
  line-height: 0.95;
}

.radar-card-effort {
  color: #dce8fb;
  font-size: 10px;
  flex: 0 0 auto;
}

.radar-card-metrics {
  display: grid;
  grid-template-rows: 1fr 1fr;
  align-items: stretch;
  border-left: 1px solid color-mix(in srgb, var(--radar-accent) 38%, #34435a);
  color: var(--radar-accent);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  font-weight: 780;
}

.radar-card-metrics strong,
.radar-card-metrics span {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 0;
  padding: 0 6px;
  white-space: nowrap;
}

.radar-card-metrics strong {
  border-bottom: 1px solid color-mix(in srgb, var(--radar-accent) 34%, #34435a);
}

.radar-chart-panel {
  margin-top: 20px;
  border: 1px solid var(--radar-line);
  border-radius: 8px;
  background: rgba(10, 22, 38, 0.9);
  padding: 16px;
}

.radar-chart-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 12px;
}

.radar-chart-title h2 {
  margin: 0;
  color: #f4f8ff;
  font-size: 16px;
  font-weight: 800;
  line-height: 1.25;
}

.radar-chart-title > span,
.radar-chart-title time {
  color: var(--radar-muted);
  font-size: 11px;
  font-weight: 650;
}

.radar-chart-title select {
  min-width: 140px;
  height: 28px;
  border: 1px solid #394c69;
  border-radius: 5px;
  background: #0c1a2d;
  color: #dce8fb;
  font: inherit;
  font-size: 11px;
  font-weight: 700;
  padding: 0 8px;
}

.radar-chart-title select:disabled {
  cursor: default;
  opacity: 1;
}

.radar-legend {
  justify-content: flex-end;
  gap: 10px;
}

.radar-legend span {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: #c2d0e4;
  font-size: 11px;
  font-weight: 650;
}

.radar-legend span::before {
  width: 10px;
  height: 2px;
  border-radius: 1px;
  background: var(--radar-legend-color);
  box-shadow: 0 0 8px var(--radar-legend-color);
  content: '';
}

.radar-chart-canvas {
  position: relative;
  height: 430px;
}

.radar-attribution {
  margin: 10px 0 0;
  color: #8092ae;
  font-size: 11px;
  line-height: 1.4;
  text-align: right;
}

.radar-attribution a {
  color: #9cb9e7;
  text-decoration: none;
}

.radar-attribution a:hover {
  color: #dce8fb;
  text-decoration: underline;
}

.radar-chart-empty,
.radar-state {
  display: grid;
  place-items: center;
  min-height: 240px;
  border: 1px solid var(--radar-line);
  border-radius: 8px;
  background: rgba(10, 22, 38, 0.9);
  color: var(--radar-muted);
  font-size: 14px;
}

.radar-error {
  align-content: center;
  gap: 9px;
  padding: 28px;
  color: #dce8fb;
}

.radar-error span {
  color: #94a9c7;
  font-size: 12px;
}

.radar-retry-button {
  border: 1px solid #3d82ff;
  border-radius: 6px;
  background: #163b75;
  color: #eaf2ff;
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  font-weight: 760;
  padding: 7px 10px;
}

.radar-retry-button:hover {
  background: #1d4a90;
}

@keyframes radar-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 1120px) {
  .radar-card-group {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 760px) {
  .radar-page {
    padding: 20px 16px 32px;
  }

  .radar-head,
  .radar-chart-head {
    align-items: stretch;
    flex-direction: column;
  }

  .radar-callout {
    max-width: none;
    text-align: left;
  }

  .radar-card-group {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .radar-card-iq {
    font-size: 30px;
  }

  .radar-legend {
    justify-content: flex-start;
  }

  .radar-chart-canvas {
    height: 350px;
  }
}

@media (max-width: 460px) {
  .radar-card-group {
    grid-template-columns: minmax(0, 1fr);
  }

  .radar-chart-title select {
    min-width: 0;
  }
}
</style>
