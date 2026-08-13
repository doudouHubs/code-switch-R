<script setup lang="ts">
import { computed } from "vue";

type HighlightSegment = {
  text: string;
  matched: boolean;
};

const props = defineProps<{
  text: string;
  keyword: string;
}>();

const segments = computed<HighlightSegment[]>(() => {
  const keyword = props.keyword.trim();
  if (!keyword) {
    return [{ text: props.text, matched: false }];
  }

  const normalizedText = props.text.toLocaleLowerCase();
  const normalizedKeyword = keyword.toLocaleLowerCase();
  const result: HighlightSegment[] = [];
  let cursor = 0;

  // 按原始文本切片而不是拼接 v-html，既保留原大小写，
  // 也避免项目路径和会话内容进入 HTML 注入链路。
  while (cursor < props.text.length) {
    const matchIndex = normalizedText.indexOf(normalizedKeyword, cursor);
    if (matchIndex < 0) {
      result.push({ text: props.text.slice(cursor), matched: false });
      break;
    }
    if (matchIndex > cursor) {
      result.push({
        text: props.text.slice(cursor, matchIndex),
        matched: false,
      });
    }
    const matchEnd = matchIndex + keyword.length;
    result.push({
      text: props.text.slice(matchIndex, matchEnd),
      matched: true,
    });
    cursor = matchEnd;
  }

  return result.length > 0 ? result : [{ text: props.text, matched: false }];
});
</script>

<template>
  <span class="card-highlighted-text">
    <template v-for="(segment, index) in segments" :key="index">
      <mark v-if="segment.matched" class="card-search-mark">{{ segment.text }}</mark>
      <template v-else>{{ segment.text }}</template>
    </template>
  </span>
</template>
