<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useUIStore } from '../../stores/ui'
import { Activity, Cpu, Database, X } from 'lucide-vue-next'

const uiStore = useUIStore()

// Debug panel visibility (controlled by URL hash or env)
const isVisible = ref(false)
const isMinimized = ref(false)

// Memory metrics
const jsHeapSize = ref<number | null>(null)
const domNodeCount = ref(0)
const pollingStatus = ref('unknown')

// Update interval
let updateTimer: ReturnType<typeof setInterval> | null = null

const formatBytes = (bytes: number | null): string => {
  if (bytes === null) return 'N/A'
  const mb = bytes / (1024 * 1024)
  return `${mb.toFixed(2)} MB`
}

const updateMetrics = () => {
  // JS Heap Size (Chrome/Edge only)
  const perf = performance as Performance & {
    memory?: { usedJSHeapSize: number; totalJSHeapSize: number }
  }
  if (perf.memory) {
    jsHeapSize.value = perf.memory.usedJSHeapSize
  }

  // DOM Node Count
  domNodeCount.value = document.getElementsByTagName('*').length
}

const toggleMinimize = () => {
  isMinimized.value = !isMinimized.value
}

const close = () => {
  isVisible.value = false
  window.location.hash = ''
}

onMounted(() => {
  // Check URL hash for debug mode
  const checkHash = () => {
    isVisible.value = window.location.hash === '#debug'
  }
  checkHash()
  window.addEventListener('hashchange', checkHash)

  // Start metrics update
  updateMetrics()
  updateTimer = setInterval(updateMetrics, 1000)
})

onUnmounted(() => {
  if (updateTimer) {
    clearInterval(updateTimer)
  }
})

const themeDisplay = computed(() => uiStore.themeMode)
const skinDisplay = computed(() => uiStore.skinId)
const densityDisplay = computed(() => uiStore.density)
const effectsDisplay = computed(() => uiStore.effects)
</script>

<template>
  <Teleport to="body">
    <Transition name="slide">
      <div
        v-if="isVisible"
        class="fixed bottom-4 right-4 z-[200] font-mono text-xs"
        :class="isMinimized ? 'w-auto' : 'w-72'"
      >
        <div
          class="bg-black/90 backdrop-blur-md border border-white/10 rounded-lg overflow-hidden shadow-2xl"
        >
          <!-- Header -->
          <div
            class="flex items-center justify-between px-3 py-2 bg-white/5 border-b border-white/10 cursor-pointer"
            @click="toggleMinimize"
          >
            <div class="flex items-center gap-2">
              <Activity :size="12" class="text-emerald-400" />
              <span class="text-white/80 font-bold">Debug Panel</span>
            </div>
            <button
              class="p-1 hover:bg-white/10 rounded transition-colors"
              @click.stop="close"
            >
              <X :size="12" class="text-white/50" />
            </button>
          </div>

          <!-- Content -->
          <div v-if="!isMinimized" class="p-3 space-y-3">
            <!-- Memory Section -->
            <div class="space-y-1">
              <div class="flex items-center gap-2 text-white/50">
                <Cpu :size="10" />
                <span>Memory</span>
              </div>
              <div class="pl-4 space-y-1">
                <div class="flex justify-between">
                  <span class="text-white/40">JS Heap:</span>
                  <span class="text-emerald-400">{{ formatBytes(jsHeapSize) }}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-white/40">DOM Nodes:</span>
                  <span class="text-cyan-400">{{ domNodeCount }}</span>
                </div>
              </div>
            </div>

            <!-- UI State Section -->
            <div class="space-y-1">
              <div class="flex items-center gap-2 text-white/50">
                <Database :size="10" />
                <span>UI State</span>
              </div>
              <div class="pl-4 space-y-1">
                <div class="flex justify-between">
                  <span class="text-white/40">Theme:</span>
                  <span class="text-purple-400">{{ themeDisplay }}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-white/40">Skin:</span>
                  <span class="text-purple-400">{{ skinDisplay }}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-white/40">Density:</span>
                  <span class="text-purple-400">{{ densityDisplay }}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-white/40">Effects:</span>
                  <span class="text-purple-400">{{ effectsDisplay }}</span>
                </div>
              </div>
            </div>

            <!-- Instructions -->
            <div class="text-[10px] text-white/30 pt-2 border-t border-white/5">
              Switch themes 10x to test memory stability
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.slide-enter-active,
.slide-leave-active {
  transition: all 0.3s ease;
}

.slide-enter-from,
.slide-leave-to {
  opacity: 0;
  transform: translateY(20px);
}
</style>
