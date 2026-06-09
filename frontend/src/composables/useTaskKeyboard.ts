import { ref } from 'vue'

export interface UseTaskKeyboardOptions {
  shouldHandle: () => boolean
  onEscape: () => void
  onSelectAll: () => void
}

export function useTaskKeyboard(options: UseTaskKeyboardOptions) {
  const isKeydownActive = ref(false)

  const isEditableTarget = (target: EventTarget | null): boolean => {
    let el = target as HTMLElement | null
    while (el) {
      const tag = (el.tagName || '').toLowerCase()
      if (tag === 'input' || tag === 'textarea' || el.isContentEditable) return true
      el = el.parentElement
    }
    return false
  }

  const handleKeydown = (e: KeyboardEvent) => {
    if (!options.shouldHandle()) return

    // Escape: Close modal or clear selection
    if (e.key === 'Escape') {
      options.onEscape()
      return
    }

    // Ctrl+A / Cmd+A: Select all visible tasks
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
      // If user is typing in an input/search box, keep native select-all behavior
      if (isEditableTarget(e.target) || isEditableTarget(document.activeElement)) return

      e.preventDefault()
      options.onSelectAll()
    }
  }

  const activateKeydown = () => {
    if (isKeydownActive.value || !options.shouldHandle()) return
    window.addEventListener('keydown', handleKeydown)
    isKeydownActive.value = true
  }

  const deactivateKeydown = () => {
    if (!isKeydownActive.value) return
    window.removeEventListener('keydown', handleKeydown)
    isKeydownActive.value = false
  }

  return {
    isKeydownActive,
    activateKeydown,
    deactivateKeydown,
  }
}
