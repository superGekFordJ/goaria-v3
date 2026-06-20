import { ref, watch } from 'vue'
import { Clipboard, Events } from '@wailsio/runtime'
import { useTaskStore } from '../stores/task'
import { useUIStore } from '../stores/ui'
import { isValidUrl, isDuplicateUri } from '../utils/url'

export function useSmartInput() {
  const uiStore = useUIStore()
  const taskStore = useTaskStore()

  const isDragging = ref(false)
  let unsubs: Array<() => void> = []

  let lastClipboardCandidate = ''
  let dragCounter = 0

  const processDroppedText = (text: string) => {
    const lines = text
      .split('\n')
      .map(l => l.trim())
      .filter(Boolean)
    const validUrls = lines.filter(isValidUrl)
    if (validUrls.length === 0) return

    if (uiStore.activeTab !== 'downloads') {
      uiStore.setActiveTab('downloads')
    }

    if (validUrls.length === 1) {
      uiStore.setPendingPasteUri(validUrls[0])
    } else {
      uiStore.setPendingPasteUris(validUrls)
    }
  }

  const processClipboard = async (trigger: 'auto' | 'manual') => {
    try {
      const rawText = (await Clipboard.Text()).trim()
      if (!rawText) return

      // In auto mode, if clipboard hasn't changed, ignore completely
      if (trigger === 'auto' && rawText === lastClipboardCandidate) {
        return
      }

      lastClipboardCandidate = rawText

      const lines = rawText
        .split('\n')
        .map(l => l.trim())
        .filter(Boolean)
      const validUrls = lines.filter(isValidUrl)

      if (validUrls.length === 0) return

      // Check for duplicates - exactly matching the original logic but supporting batch iteration
      const newUrls = validUrls.filter(u => !isDuplicateUri(u, taskStore))

      // If every url we found is a duplicate, we abort to prevent popup/noise
      if (newUrls.length === 0) return

      // For auto mode, only switch if there's actually a NEW url found
      if (trigger === 'auto') {
        if (uiStore.activeTab !== 'downloads') {
          uiStore.setActiveTab('downloads')
        }
      }

      // Populate text regardless of duplicates if it makes it this far (the TaskHeader will show duplicates counts before submitting to backend)
      if (uiStore.activeTab === 'downloads') {
        if (validUrls.length === 1) {
          uiStore.setPendingPasteUri(validUrls[0])
        } else {
          uiStore.setPendingPasteUris(validUrls)
        }
      }
    } catch {
      // Permission denied or empty clipboard, silently ignore
    }
  }

  const handleDragEnter = (e: DragEvent) => {
    e.preventDefault()
    dragCounter++
    if (dragCounter === 1) isDragging.value = true
  }

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
  }

  const handleDragLeave = (e: DragEvent) => {
    e.preventDefault()
    dragCounter--
    if (dragCounter <= 0) {
      dragCounter = 0
      isDragging.value = false
    }
  }

  const handleDrop = (e: DragEvent) => {
    e.preventDefault()
    dragCounter = 0
    isDragging.value = false

    const files = e.dataTransfer?.files
    if (files && files.length > 0) {
      const txtFiles = Array.from(files).filter(f => f.name.toLowerCase().endsWith('.txt'))
      if (txtFiles.length > 0) {
        let remaining = txtFiles.length
        let allContent = ''
        for (const file of txtFiles) {
          const reader = new FileReader()
          reader.onload = ev => {
            allContent += (ev.target?.result as string) || ''
            remaining--
            if (remaining === 0) processDroppedText(allContent)
          }
          reader.readAsText(file)
        }
        return
      }
    }

    const text = e.dataTransfer?.getData('text/plain')?.trim()
    if (text) processDroppedText(text)
  }

  const initSmartInput = () => {
    unsubs.push(
      Events.On('common:WindowFocus', () => {
        processClipboard('auto')
      }),
    )

    watch(
      () => uiStore.activeTab,
      newTab => {
        if (newTab === 'downloads') {
          processClipboard('manual')
        }
      },
    )

    document.addEventListener('dragenter', handleDragEnter)
    document.addEventListener('dragover', handleDragOver)
    document.addEventListener('dragleave', handleDragLeave)
    document.addEventListener('drop', handleDrop)

    unsubs.push(() => {
      document.removeEventListener('dragenter', handleDragEnter)
      document.removeEventListener('dragover', handleDragOver)
      document.removeEventListener('dragleave', handleDragLeave)
      document.removeEventListener('drop', handleDrop)
    })
  }

  const cleanupSmartInput = () => {
    unsubs.forEach(unsub => unsub())
    unsubs = []
  }

  return {
    isDragging,
    initSmartInput,
    cleanupSmartInput,
  }
}
