import { mount } from 'svelte'
import Popup from './Popup.svelte'
import '../styles/index.css'
import { initStatusListener } from '../stores/connection-popup'

// Permanent safety net: render a small message if the popup fails to mount.
window.addEventListener('error', e => {
  const app = document.getElementById('app')
  if (app && !app.childElementCount) {
    app.innerHTML = `<div style="color:#e4e4e7;padding:8px;font-size:12px;font-family:system-ui,sans-serif;">Popup failed to load: ${e.message}</div>`
  }
})

// Subscribe to background WS status pushes and fetch the current snapshot
// before mounting so the popup renders with fresh state.
void initStatusListener()

const target = document.getElementById('app')
if (!target) throw new Error('#app element not found')

export default mount(Popup, { target })
