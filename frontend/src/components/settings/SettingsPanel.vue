<script setup lang="ts">
  import { ref, onMounted, onUnmounted } from 'vue'
  import { useConfigStore } from '../../stores/config'
  import { useUIStore, type ThemeMode, type SkinId } from '../../stores/ui'
  import {
    Settings as SettingsIcon,
    Shield,
    Cpu,
    Globe,
    History,
    FolderOpen,
    Zap,
    CheckCircle,
    Loader2,
    Sun,
    Moon,
    Monitor,
    Palette,
    Layers,
  } from 'lucide-vue-next'

  const configStore = useConfigStore()
  const uiStore = useUIStore()

  // Local form state - decoupled from store to prevent reactivity issues
  const formData = ref({
    download_dir: '',
    rpc_port: '',
    rpc_secret: '',
    max_connections: '',
    max_concurrent_downloads: '',
    user_agent: '',
    show_history: false,
    window_transparency: 'none',
  })

  // Save status for UI feedback only
  const saveStatus = ref<'idle' | 'saving' | 'saved'>('idle')
  let saveTimeout: ReturnType<typeof setTimeout> | null = null
  let statusResetTimeout: ReturnType<typeof setTimeout> | null = null
  let isInitialized = false

  // Initialize form data from store
  onMounted(() => {
    const s = configStore.settings
    formData.value = {
      download_dir: s.download_dir || '',
      rpc_port: String(s.rpc_port || ''),
      rpc_secret: s.rpc_secret || '',
      max_connections: String(s.max_connections || ''),
      max_concurrent_downloads: String(s.max_concurrent_downloads || ''),
      user_agent: s.user_agent || '',
      show_history: Boolean(s.show_history),
      window_transparency: (s as any).window_transparency || 'none',
    }
    // Mark as initialized after a tick to avoid triggering save on mount
    setTimeout(() => {
      isInitialized = true
    }, 100)
  })

  // Non-blocking background save function
  const triggerSave = () => {
    if (!isInitialized) return

    // Clear any pending operations
    if (saveTimeout) clearTimeout(saveTimeout)
    if (statusResetTimeout) clearTimeout(statusResetTimeout)

    // Show saving indicator
    saveStatus.value = 'saving'

    // Debounce - fire and forget
    saveTimeout = setTimeout(() => {
      // Copy form data to store
      Object.assign(configStore.settings, formData.value)

      // Save in background - completely non-blocking
      configStore
        .updateConfig()
        .then(() => {
          saveStatus.value = 'saved'
          statusResetTimeout = setTimeout(() => {
            saveStatus.value = 'idle'
          }, 1500)
        })
        .catch(() => {
          saveStatus.value = 'idle'
        })
    }, 800)
  }

  // Handle directory picker
  const handlePickDirectory = async () => {
    await configStore.pickDirectory()
    // Sync the new value
    formData.value.download_dir = configStore.settings.download_dir || ''
    triggerSave()
  }

  // Toggle history setting
  const toggleHistory = () => {
    formData.value.show_history = !formData.value.show_history
    triggerSave()
  }

  // Cleanup timers on unmount
  onUnmounted(() => {
    if (saveTimeout) clearTimeout(saveTimeout)
    if (statusResetTimeout) clearTimeout(statusResetTimeout)
  })

  // Connection options
  const connectionOptions = ['1', '4', '8', '16']
</script>

<template>
  <div class="flex-1 overflow-y-auto p-6 animate-fade-in-up">
    <div class="max-w-2xl mx-auto">
      <!-- Header -->
      <div class="flex items-center justify-between mb-8">
        <div class="flex items-center gap-4">
          <div
            class="w-12 h-12 rounded-[var(--radius-squircle-md)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] flex items-center justify-center"
          >
            <SettingsIcon :size="22" class="text-[var(--app-text-muted)]" />
          </div>
          <div>
            <h2 class="text-2xl font-bold text-[var(--app-text)] tracking-tight">偏好设置</h2>
            <p class="text-xs text-[var(--app-text-subtle)] mt-0.5">配置您的下载偏好</p>
          </div>
        </div>

        <!-- Save Status Indicator -->
        <div class="flex items-center gap-2">
          <Transition name="fade" mode="out-in">
            <div
              v-if="saveStatus === 'saving'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]"
            >
              <Loader2 :size="12" class="animate-spin text-[var(--neon-primary)]" />
              <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">保存中...</span>
            </div>
            <div
              v-else-if="saveStatus === 'saved'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--status-active)]/10 border border-[var(--status-active)]/20"
            >
              <CheckCircle :size="12" class="text-[var(--status-active)]" />
              <span class="text-[10px] font-mono-data text-[var(--status-active)]">已保存</span>
            </div>
            <div
              v-else
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]"
            >
              <div class="w-1.5 h-1.5 rounded-full bg-[var(--app-text-subtle)]"></div>
              <span class="text-[10px] font-mono-data text-[var(--app-text-subtle)]">自动保存</span>
            </div>
          </Transition>
        </div>
      </div>

      <!-- Settings Cards Container -->
      <div class="space-y-4">
        <!-- Download Directory Card -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-4">
            <div
              class="w-8 h-8 rounded-xl bg-[var(--neon-primary)]/10 flex items-center justify-center"
            >
              <FolderOpen :size="16" class="text-[var(--neon-primary)]" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">下载目录</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">文件默认保存位置</p>
            </div>
          </div>

          <div
            class="flex gap-3 p-1.5 bg-[var(--input-bg)] rounded-[var(--radius-squircle-md)] border border-[var(--input-border)]"
          >
            <input
              v-model="formData.download_dir"
              readonly
              class="flex-1 bg-transparent px-4 py-2.5 text-sm font-mono-data text-[var(--app-text)]/70 outline-none cursor-default truncate"
              :title="formData.download_dir"
            />
            <button
              class="btn-neon px-5 py-2 rounded-xl text-xs font-bold shrink-0"
              @click="handlePickDirectory"
            >
              浏览
            </button>
          </div>
        </div>

        <!-- RPC Configuration Card -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-6">
            <div class="w-8 h-8 rounded-xl bg-purple-500/10 flex items-center justify-center">
              <Zap :size="16" class="text-purple-400" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">RPC 连接</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">Aria2 后端通信配置</p>
            </div>
          </div>

          <div class="grid grid-cols-2 gap-4">
            <!-- RPC Port -->
            <div class="space-y-2">
              <label
                class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
              >
                <Globe :size="10" />
                RPC 端口
              </label>
              <input
                v-model="formData.rpc_port"
                type="text"
                class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
                @input="triggerSave"
              />
            </div>

            <!-- RPC Secret -->
            <div class="space-y-2">
              <label
                class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
              >
                <Shield :size="10" />
                RPC 密钥
              </label>
              <div class="relative">
                <input
                  v-model="formData.rpc_secret"
                  type="password"
                  placeholder="可选"
                  class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)] placeholder:text-[var(--input-placeholder)]"
                  @input="triggerSave"
                />
              </div>
            </div>
          </div>
        </div>

        <!-- Performance Card -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-6">
            <div class="w-8 h-8 rounded-xl bg-amber-500/10 flex items-center justify-center">
              <Cpu :size="16" class="text-amber-400" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">性能设置</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">优化下载速度和资源占用</p>
            </div>
          </div>

          <div class="grid grid-cols-2 gap-4">
            <!-- Max Connections Per Server -->
            <div class="space-y-2">
              <label
                class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
              >
                单任务连接数
              </label>
              <div class="relative">
                <select
                  v-model="formData.max_connections"
                  class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none appearance-none cursor-pointer transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
                  @change="triggerSave"
                >
                  <option v-for="n in connectionOptions" :key="n" :value="n">{{ n }} 线程</option>
                </select>
                <!-- Custom dropdown arrow -->
                <div class="absolute right-4 top-1/2 -translate-y-1/2 pointer-events-none">
                  <svg
                    width="10"
                    height="6"
                    viewBox="0 0 10 6"
                    fill="none"
                    class="text-[var(--app-text-subtle)]"
                  >
                    <path
                      d="M1 1L5 5L9 1"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    />
                  </svg>
                </div>
              </div>
            </div>

            <!-- Max Concurrent Downloads -->
            <div class="space-y-2">
              <label
                class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
              >
                最大并发任务
              </label>
              <input
                v-model="formData.max_concurrent_downloads"
                type="number"
                min="1"
                max="10"
                class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
                @input="triggerSave"
              />
            </div>
          </div>
        </div>

        <!-- User Agent Card -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-4">
            <div class="w-8 h-8 rounded-xl bg-blue-500/10 flex items-center justify-center">
              <Globe :size="16" class="text-blue-400" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">User-Agent</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">自定义浏览器标识</p>
            </div>
          </div>

          <textarea
            v-model="formData.user_agent"
            rows="2"
            placeholder="留空使用默认值"
            class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-[11px] font-mono-data text-[var(--app-text)]/70 outline-none resize-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)] placeholder:text-[var(--input-placeholder)]"
            @input="triggerSave"
          ></textarea>
        </div>

        <!-- Theme & Appearance Card -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-6">
            <div class="w-8 h-8 rounded-xl bg-indigo-500/10 flex items-center justify-center">
              <Palette :size="16" class="text-indigo-400" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">外观设置</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">主题模式与皮肤风格</p>
            </div>
          </div>

          <!-- Theme Mode Selector -->
          <div class="mb-6">
            <label
              class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
            >
              主题模式
            </label>
            <div class="grid grid-cols-3 gap-2">
              <button
                v-for="mode in ['system', 'light', 'dark'] as ThemeMode[]"
                :key="mode"
                :class="[
                  'flex flex-col items-center gap-2 p-4 rounded-xl border transition-all duration-200',
                  uiStore.themeMode === mode
                    ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30 text-[var(--neon-primary)]'
                    : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] text-[var(--app-text-muted)] hover:border-[var(--neon-primary)]/20',
                ]"
                @click="uiStore.setTheme(mode)"
              >
                <Monitor v-if="mode === 'system'" :size="20" />
                <Sun v-else-if="mode === 'light'" :size="20" />
                <Moon v-else :size="20" />
                <span class="text-[10px] font-semibold">
                  {{ mode === 'system' ? '跟随系统' : mode === 'light' ? '亮色' : '暗色' }}
                </span>
              </button>
            </div>
          </div>

          <!-- Skin Selector -->
          <div>
            <label
              class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
            >
              皮肤风格
            </label>
            <div class="grid grid-cols-2 gap-3">
              <button
                v-for="skin in ['obsidian', 'ceramic'] as SkinId[]"
                :key="skin"
                :class="[
                  'flex items-center gap-3 p-4 rounded-xl border transition-all duration-200',
                  uiStore.skinId === skin
                    ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
                    : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
                ]"
                @click="uiStore.setSkin(skin)"
              >
                <div
                  :class="[
                    'w-8 h-8 rounded-lg',
                    skin === 'obsidian'
                      ? 'bg-gradient-to-br from-gray-800 to-gray-900'
                      : 'bg-gradient-to-br from-gray-100 to-white border border-gray-200',
                  ]"
                ></div>
                <div class="text-left">
                  <span
                    :class="[
                      'text-xs font-semibold block',
                      uiStore.skinId === skin
                        ? 'text-[var(--neon-primary)]'
                        : 'text-[var(--app-text)]/80',
                    ]"
                  >
                    {{ skin === 'obsidian' ? 'Obsidian' : 'Ceramic' }}
                  </span>
                  <span class="text-[9px] text-[var(--app-text-subtle)]">
                    {{ skin === 'obsidian' ? '深邃黑曜石' : '温润陶瓷白' }}
                  </span>
                </div>
              </button>
            </div>
          </div>
        </div>

        <!-- Window Transparency Card (Windows 11 only) -->
        <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
          <div class="flex items-center gap-3 mb-4">
            <div class="w-8 h-8 rounded-xl bg-cyan-500/10 flex items-center justify-center">
              <Layers :size="16" class="text-cyan-400" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-[var(--app-text)]/80">窗口透明效果</h3>
              <p class="text-[10px] text-[var(--app-text-subtle)]">仅 Windows 11 支持，更改后需重启应用</p>
            </div>
          </div>

          <div class="grid grid-cols-2 gap-3">
            <button
              v-for="opt in [
                { value: 'none', label: '关闭', desc: '标准窗口' },
                { value: 'acrylic', label: '亚克力', desc: 'Acrylic 模糊' },
                { value: 'mica', label: '云母', desc: 'Mica 材质' },
                { value: 'tabbed', label: 'Tabbed', desc: '标签页风格' },
              ]"
              :key="opt.value"
              :class="[
                'flex flex-col items-start p-4 rounded-xl border transition-all duration-200',
                formData.window_transparency === opt.value
                  ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
                  : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
              ]"
              @click="formData.window_transparency = opt.value; triggerSave()"
            >
              <span
                :class="[
                  'text-xs font-semibold',
                  formData.window_transparency === opt.value
                    ? 'text-[var(--neon-primary)]'
                    : 'text-[var(--app-text)]/80',
                ]"
              >
                {{ opt.label }}
              </span>
              <span class="text-[9px] text-[var(--app-text-subtle)]">{{ opt.desc }}</span>
            </button>
          </div>
        </div>

        <!-- History Toggle Card -->
        <div
          class="glass-panel rounded-[var(--radius-squircle-lg)] p-6 cursor-pointer transition-all duration-300 hover:border-[var(--neon-primary)]/20"
          @click="toggleHistory"
        >
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div
                :class="[
                  'w-8 h-8 rounded-xl flex items-center justify-center transition-colors duration-300',
                  formData.show_history
                    ? 'bg-[var(--neon-primary)]/10'
                    : 'bg-[var(--btn-glass-bg)]',
                ]"
              >
                <History
                  :size="16"
                  :class="
                    formData.show_history
                      ? 'text-[var(--neon-primary)]'
                      : 'text-[var(--app-text-subtle)]'
                  "
                />
              </div>
              <div>
                <h3 class="text-sm font-semibold text-[var(--app-text)]/80">显示下载历史</h3>
                <p class="text-[10px] text-[var(--app-text-subtle)]">
                  在"已完成"标签页显示历史记录
                </p>
              </div>
            </div>

            <!-- Toggle Switch -->
            <div
              :class="[
                'w-12 h-7 rounded-full relative transition-all duration-300 cursor-pointer',
                formData.show_history ? 'bg-[var(--neon-primary)]' : 'bg-[var(--btn-glass-bg)]',
              ]"
            >
              <div
                :class="[
                  'absolute top-1 w-5 h-5 rounded-full bg-white shadow-lg transition-all duration-300',
                  formData.show_history ? 'left-6' : 'left-1',
                ]"
              ></div>
            </div>
          </div>
        </div>

        <!-- About / Version Info -->
        <div class="glass-panel-subtle rounded-[var(--radius-squircle-lg)] p-5 mt-6">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div
                class="w-8 h-8 rounded-xl bg-gradient-to-br from-[var(--neon-primary)] to-[var(--neon-secondary)] flex items-center justify-center"
              >
                <Zap :size="14" class="text-[var(--app-bg)]" />
              </div>
              <div>
                <span class="text-sm font-bold text-[var(--app-text)]/60">GoAria</span>
                <span class="text-[10px] text-[var(--app-text-subtle)] ml-2 font-mono-data"
                  >Luminous Edition</span
                >
              </div>
            </div>
            <div class="text-[10px] font-mono-data text-[var(--app-text-subtle)]/50">
              Powered by Aria2 + Wails
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
  /* Fade transition for save status */
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.2s ease;
  }

  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }

  /* Select option styling (limited support) */
  select option {
    background: var(--glass-bg);
    color: var(--app-text);
    padding: 8px;
  }

  /* Hide number input spinners */
  input[type='number']::-webkit-inner-spin-button,
  input[type='number']::-webkit-outer-spin-button {
    -webkit-appearance: none;
    margin: 0;
  }

  input[type='number'] {
    -moz-appearance: textfield;
  }

  /* Textarea scrollbar */
  textarea::-webkit-scrollbar {
    width: 4px;
  }

  textarea::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.1);
    border-radius: 4px;
  }
</style>
