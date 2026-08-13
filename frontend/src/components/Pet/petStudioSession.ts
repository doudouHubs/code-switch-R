import { reactive, computed } from 'vue'
import {
  createBlankPetStudioProject,
  fingerprintPetStudioProject,
  petStudioProjectReducer,
  type PetStudioProject,
  type PetStudioProjectAction
} from './petStudioModel'

export type PetStudioEditorMode = 'actions' | 'behaviors'

export interface PetStudioReferenceImage {
  data: string
  mediaType: string
  preview: string
}

const MAX_REFERENCE_IMAGES = 3

export function createPetStudioSession() {
  const initialProject = createBlankPetStudioProject()
  const state = reactive({
    project: initialProject,
    baselineProject: initialProject,
    baselineFingerprint: fingerprintPetStudioProject(initialProject),
    sourceSelection: 'new',
    editorMode: 'actions' as PetStudioEditorMode,
    selectedPose: 'idle',
    selectedFrameId: null as string | null,
    selectedBehaviorId: 'feed',
    referenceImages: [] as PetStudioReferenceImage[],
    // 旧页面仍读取单数引用；它始终指向第一张，避免出现两份可写状态。
    referenceImage: null as PetStudioReferenceImage | null,
    saving: false,
    saveRevision: 0
  })

  const dirty = computed(() => fingerprintPetStudioProject(state.project) !== state.baselineFingerprint)

  function dispatchProject(action: PetStudioProjectAction): PetStudioProject {
    const next = petStudioProjectReducer(state.project, action)
    state.project = next
    return next
  }

  function loadProject(project: PetStudioProject, sourceSelection: string): void {
    state.project = project
    state.baselineProject = project
    state.baselineFingerprint = fingerprintPetStudioProject(project)
    state.sourceSelection = sourceSelection
    state.selectedPose = 'idle'
    state.selectedFrameId = project.animations.idle?.frames[0]?.id ?? null
    state.selectedBehaviorId = Object.keys(project.behaviors)[0] ?? 'feed'
    state.referenceImages = []
    state.referenceImage = null
  }

  function beginSave(): number | null {
    if (state.saving) return null
    state.saving = true
    state.saveRevision += 1
    return state.saveRevision
  }

  function finishSave(revision: number): void {
    if (revision === state.saveRevision) state.saving = false
  }

  function commitSavedProject(revision: number, project: PetStudioProject, sourceSelection: string): boolean {
    if (revision !== state.saveRevision) return false
    state.project = project
    state.baselineProject = project
    state.baselineFingerprint = fingerprintPetStudioProject(project)
    state.sourceSelection = sourceSelection
    state.saving = false
    return true
  }

  function setReferenceImages(images: readonly PetStudioReferenceImage[]): void {
    const next = images.slice(0, MAX_REFERENCE_IMAGES).map((image) => ({ ...image }))
    state.referenceImages = next
    state.referenceImage = next[0] ?? null
  }

  return {
    state,
    dirty,
    dispatchProject,
    loadProject,
    beginSave,
    finishSave,
    commitSavedProject,
    setReferenceImages,
    setSession(patch: Partial<typeof state>) {
      const next = { ...patch }
      if (patch.referenceImages !== undefined) {
        const images = patch.referenceImages.slice(0, MAX_REFERENCE_IMAGES).map((image) => ({ ...image }))
        next.referenceImages = images
        next.referenceImage = images[0] ?? null
      } else if (patch.referenceImage !== undefined) {
        const image = patch.referenceImage ? { ...patch.referenceImage } : null
        next.referenceImages = image ? [image] : []
        next.referenceImage = image
      }
      Object.assign(state, next)
    }
  }
}
