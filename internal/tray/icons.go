package tray

// Tray icons as SVG data - optimized for 16x16 pixel clarity
// Using bold, clear shapes that remain legible at small sizes

// IconIdle - Minimalist download arrow logo (single color, neutral)
// A bold downward arrow inside a rounded container
var IconIdle = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <linearGradient id="idle-grad" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" style="stop-color:#a1a1aa"/>
      <stop offset="100%" style="stop-color:#71717a"/>
    </linearGradient>
  </defs>
  <!-- Rounded square background -->
  <rect x="1" y="1" width="14" height="14" rx="3" fill="url(#idle-grad)"/>
  <!-- Bold down arrow -->
  <path d="M8 3.5v6M5 7l3 3.5 3-3.5" stroke="#18181b" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
  <!-- Base line -->
  <path d="M4.5 12.5h7" stroke="#18181b" stroke-width="1.5" stroke-linecap="round"/>
</svg>`)

// IconActive - Download in progress (cyan/emerald glow indicating data flow)
// Adds a pulsing cyan accent to show active downloading
var IconActive = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <linearGradient id="active-grad" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" style="stop-color:#06ffd5"/>
      <stop offset="100%" style="stop-color:#22ff88"/>
    </linearGradient>
    <filter id="glow">
      <feGaussianBlur stdDeviation="0.5" result="coloredBlur"/>
      <feMerge>
        <feMergeNode in="coloredBlur"/>
        <feMergeNode in="SourceGraphic"/>
      </feMerge>
    </filter>
  </defs>
  <!-- Glowing rounded square background -->
  <rect x="1" y="1" width="14" height="14" rx="3" fill="url(#active-grad)" filter="url(#glow)"/>
  <!-- Bold down arrow - dark for contrast -->
  <path d="M8 3.5v6M5 7l3 3.5 3-3.5" stroke="#0a3d36" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
  <!-- Base line -->
  <path d="M4.5 12.5h7" stroke="#0a3d36" stroke-width="1.5" stroke-linecap="round"/>
  <!-- Activity indicator dot -->
  <circle cx="13" cy="3" r="2" fill="#22ff88"/>
</svg>`)

// IconPaused - Download paused (amber indicator)
// Shows pause state with amber coloring
var IconPaused = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <linearGradient id="paused-grad" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" style="stop-color:#fbbf24"/>
      <stop offset="100%" style="stop-color:#d97706"/>
    </linearGradient>
  </defs>
  <!-- Amber rounded square background -->
  <rect x="1" y="1" width="14" height="14" rx="3" fill="url(#paused-grad)"/>
  <!-- Pause symbol (two vertical bars) -->
  <rect x="5" y="4.5" width="2" height="7" rx="0.5" fill="#451a03"/>
  <rect x="9" y="4.5" width="2" height="7" rx="0.5" fill="#451a03"/>
</svg>`)

// IconError - Error state (red warning)
var IconError = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">
  <defs>
    <linearGradient id="error-grad" x1="0%" y1="0%" x2="0%" y2="100%">
      <stop offset="0%" style="stop-color:#f87171"/>
      <stop offset="100%" style="stop-color:#dc2626"/>
    </linearGradient>
  </defs>
  <!-- Red rounded square background -->
  <rect x="1" y="1" width="14" height="14" rx="3" fill="url(#error-grad)"/>
  <!-- Exclamation mark -->
  <path d="M8 4v5" stroke="#450a0a" stroke-width="2" stroke-linecap="round"/>
  <circle cx="8" cy="11.5" r="1.2" fill="#450a0a"/>
</svg>`)
