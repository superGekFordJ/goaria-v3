import { mount } from 'svelte'
import Popup from './Popup.svelte'
import '../styles/index.css'

const target = document.getElementById('app')
if (!target) throw new Error('#app element not found')

const app = mount(Popup, { target })

export default app
