<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type {
  CodexProjectRuntimeStatus,
  CodexSessionRuntimeStatus,
  CodexStatusMonitorInfo,
} from "../../services/projectManager";

const props = defineProps<{
  sessionStatus?: CodexSessionRuntimeStatus;
  projectStatus?: CodexProjectRuntimeStatus;
  monitor: CodexStatusMonitorInfo;
}>();

const { t } = useI18n();

const state = computed(
  () => props.projectStatus?.state ?? props.sessionStatus?.state ?? "not_loaded",
);

const stateLabel = computed(() =>
  t(`components.projectManager.codexStatus.states.${state.value}`),
);

const turnLabel = computed(() => {
  const status = props.sessionStatus?.turn_status || "unknown";
  return t(`components.projectManager.codexStatus.turn.${status}`);
});

const agentLabel = computed(() => {
  if (!props.monitor.agent_hooks_supported) {
    return t("components.projectManager.codexStatus.agent.unsupported");
  }
  if (!props.sessionStatus) {
    return t("components.projectManager.codexStatus.agent.unknown");
  }
  return t("components.projectManager.codexStatus.agent.active", {
    count: props.sessionStatus.active_agents,
  });
});

const tooltip = computed(() => {
  const lines = [
    `${t("components.projectManager.codexStatus.threadLabel")}: ${stateLabel.value}`,
    `${t("components.projectManager.codexStatus.turnLabel")}: ${turnLabel.value}`,
    `${t("components.projectManager.codexStatus.agentLabel")}: ${agentLabel.value}`,
  ];

  if (props.projectStatus) {
    lines.push(
      t("components.projectManager.codexStatus.projectSummary", {
        active: props.projectStatus.active_sessions,
        waiting: props.projectStatus.waiting_sessions,
        errors: props.projectStatus.error_sessions,
      }),
    );
  }
  if (!props.sessionStatus?.monitored && !props.monitor.installed) {
    lines.push(t("components.projectManager.codexStatus.monitorUnavailable"));
  }
  if (props.monitor.error) {
    lines.push(
      `${t("components.projectManager.codexStatus.monitorError")}: ${props.monitor.error}`,
    );
  }
  return lines.join("\n");
});
</script>

<template>
  <span
    :class="[
      'codex-status-light',
      `is-${state}`,
      {
        'is-live':
          state === 'active' ||
          state === 'waiting_approval' ||
          state === 'waiting_user_input',
      },
    ]"
    role="img"
    :title="tooltip"
    :aria-label="tooltip"
  ></span>
</template>

<style scoped>
.codex-status-light {
  position: relative;
  display: inline-block;
  width: 8px;
  height: 8px;
  flex: 0 0 8px;
  border-radius: 50%;
  background: #9ca3af;
  box-shadow: 0 0 0 2px rgba(156, 163, 175, 0.16);
}

.codex-status-light.is-idle {
  background: #22c55e;
  box-shadow: 0 0 0 2px rgba(34, 197, 94, 0.16);
}

.codex-status-light.is-active {
  background: #3b82f6;
  box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.16);
}

.codex-status-light.is-waiting_approval {
  background: #f59e0b;
  box-shadow: 0 0 0 2px rgba(245, 158, 11, 0.18);
}

.codex-status-light.is-waiting_user_input {
  background: #06b6d4;
  box-shadow: 0 0 0 2px rgba(6, 182, 212, 0.18);
}

.codex-status-light.is-system_error {
  background: #ef4444;
  box-shadow: 0 0 0 2px rgba(239, 68, 68, 0.18);
}

.codex-status-light.is-live::after {
  content: "";
  position: absolute;
  inset: -3px;
  border: 1px solid currentColor;
  border-radius: 50%;
  color: inherit;
  animation: codex-status-pulse 1.6s ease-out infinite;
}

.codex-status-light.is-active {
  color: #3b82f6;
}

.codex-status-light.is-waiting_approval {
  color: #f59e0b;
}

.codex-status-light.is-waiting_user_input {
  color: #06b6d4;
}

@keyframes codex-status-pulse {
  0% {
    opacity: 0.72;
    transform: scale(0.72);
  }
  70%,
  100% {
    opacity: 0;
    transform: scale(1.45);
  }
}

@media (prefers-reduced-motion: reduce) {
  .codex-status-light.is-live::after {
    animation: none;
  }
}
</style>
