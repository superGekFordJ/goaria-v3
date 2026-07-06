import { mount } from 'svelte'
import Popup from './Popup.svelte'
import '../styles/index.css'
import { initStatusListener } from '../stores/connection-popup'

// Subscribe to background WS status pushes and fetch the current snapshot
// before mounting so the popup renders with fresh state.
void initStatusListener()

const target = document.getElementById('app')
if (!target) throw new Error('#app element not found')

const app = mount(Popup, { target })

export default app
