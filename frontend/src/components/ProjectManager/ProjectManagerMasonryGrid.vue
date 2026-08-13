<script setup lang="ts">
import { onBeforeUnmount, onMounted, onUpdated, ref } from 'vue'

const props = withDefaults(
  defineProps<{
    minColumnWidth: number
    gap?: number
  }>(),
  {
    gap: 18,
  },
)

const containerRef = ref<HTMLElement | null>(null)
const observedItems = new Set<HTMLElement>()

let resizeObserver: ResizeObserver | null = null
let layoutFrame: number | null = null
let lastContainerWidth = -1

const resolveItems = (): HTMLElement[] => {
  const container = containerRef.value
  if (!container) {
    return []
  }

  return Array.from(container.children).filter(
    (child): child is HTMLElement => child instanceof HTMLElement,
  )
}

const layoutItems = () => {
  const container = containerRef.value
  if (!container) {
    return
  }

  const items = resolveItems()
  if (items.length === 0) {
    container.style.height = '0px'
    return
  }

  const containerWidth = container.clientWidth
  if (containerWidth <= 0) {
    // 页面切换期间容器可能暂时不可见，此时保留待测量状态，等宽度恢复后再展示。
    items.forEach((item) => item.style.setProperty('visibility', 'hidden'))
    return
  }

  lastContainerWidth = containerWidth

  const gap = Math.max(0, props.gap)
  const minColumnWidth = Math.max(1, props.minColumnWidth)
  const columnCount = Math.max(
    1,
    Math.floor((containerWidth + gap) / (minColumnWidth + gap)),
  )
  const columnWidth = (containerWidth - gap * (columnCount - 1)) / columnCount

  // 先统一写入列宽，再一次性读取高度，避免在每张卡片之间反复触发布局计算。
  items.forEach((item) => {
    item.style.position = 'absolute'
    item.style.width = `${columnWidth}px`
  })
  const itemHeights = items.map((item) => item.offsetHeight)

  const columnBottoms = Array<number>(columnCount).fill(0)
  items.forEach((item, index) => {
    // 按数据索引轮转列，牺牲少量紧凑度来保证每一轮都维持从左到右的业务顺序。
    const columnIndex = index % columnCount
    const left = columnIndex * (columnWidth + gap)
    const top = columnBottoms[columnIndex]

    item.style.left = `${left}px`
    item.style.top = `${top}px`
    item.style.removeProperty('visibility')

    columnBottoms[columnIndex] = top + itemHeights[index] + gap
  })

  const contentHeight = Math.max(...columnBottoms) - gap
  container.style.height = `${Math.max(0, contentHeight)}px`
}

const scheduleLayout = () => {
  if (layoutFrame !== null) {
    return
  }

  // ResizeObserver 可能在一次搜索更新里连续触发，合并到下一帧可避免抖动和观察器循环。
  layoutFrame = window.requestAnimationFrame(() => {
    layoutFrame = null
    layoutItems()
  })
}

const syncObservedItems = () => {
  const currentItems = new Set(resolveItems())

  observedItems.forEach((item) => {
    if (!currentItems.has(item)) {
      resizeObserver?.unobserve(item)
      observedItems.delete(item)
    }
  })

  currentItems.forEach((item) => {
    if (observedItems.has(item)) {
      return
    }

    // 新卡片在首次定位前不能堆叠到左上角，测量完成后由 layoutItems 恢复显示。
    item.style.setProperty('visibility', 'hidden')
    observedItems.add(item)
    resizeObserver?.observe(item)
  })

  scheduleLayout()
}

onMounted(() => {
  resizeObserver = new ResizeObserver((entries) => {
    const container = containerRef.value
    const shouldLayout = entries.some((entry) => {
      if (entry.target !== container) {
        return true
      }

      return Math.abs(entry.contentRect.width - lastContainerWidth) > 0.5
    })

    if (shouldLayout) {
      scheduleLayout()
    }
  })

  if (containerRef.value) {
    resizeObserver.observe(containerRef.value)
  }
  syncObservedItems()
})

// v-for 的过滤、排序和增删会复用部分 DOM，组件更新后必须重新同步观察目标与顺序。
onUpdated(syncObservedItems)

onBeforeUnmount(() => {
  if (layoutFrame !== null) {
    window.cancelAnimationFrame(layoutFrame)
  }
  resizeObserver?.disconnect()
  observedItems.clear()
})
</script>

<template>
  <section ref="containerRef" class="project-manager-masonry-grid">
    <slot />
  </section>
</template>

