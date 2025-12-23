package tray

// Tray icons as SVG data - "The Parallel Flow" Design
// Optimized for 32x32 viewbox, legible at 16x16.

var IconIdle = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" fill="none"><rect x="5" y="6" width="6" height="16" rx="3" fill="#a1a1aa"/><rect x="13" y="6" width="6" height="22" rx="3" fill="#a1a1aa"/><rect x="21" y="6" width="6" height="16" rx="3" fill="#a1a1aa"/></svg>`)

var IconActive = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="a" x1="16" y1="0" x2="16" y2="32" gradientUnits="userSpaceOnUse"><stop offset="0%" stop-color="#06ffd5"/><stop offset="100%" stop-color="#22ff88"/></linearGradient></defs><rect x="5" y="6" width="6" height="16" rx="3" fill="url(#a)"/><rect x="13" y="6" width="6" height="22" rx="3" fill="url(#a)"/><rect x="21" y="6" width="6" height="16" rx="3" fill="url(#a)"/><circle cx="26" cy="26" r="3" fill="#22ff88"/></svg>`)

var IconPaused = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="p" x1="16" y1="0" x2="16" y2="32" gradientUnits="userSpaceOnUse"><stop offset="0%" stop-color="#fbbf24"/><stop offset="100%" stop-color="#d97706"/></linearGradient></defs><rect x="5" y="6" width="6" height="16" rx="3" fill="url(#p)"/><rect x="13" y="6" width="6" height="22" rx="3" fill="url(#p)"/><rect x="21" y="6" width="6" height="16" rx="3" fill="url(#p)"/></svg>`)

var IconError = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="e" x1="16" y1="0" x2="16" y2="32" gradientUnits="userSpaceOnUse"><stop offset="0%" stop-color="#f43f5e"/><stop offset="100%" stop-color="#e11d48"/></linearGradient></defs><rect x="5" y="6" width="6" height="16" rx="3" fill="url(#e)"/><rect x="13" y="6" width="6" height="22" rx="3" fill="url(#e)"/><rect x="21" y="6" width="6" height="16" rx="3" fill="url(#e)"/><circle cx="26" cy="26" r="3" fill="#f43f5e"/></svg>`)
