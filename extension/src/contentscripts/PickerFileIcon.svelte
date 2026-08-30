<script lang="ts">
  import type { PickerCategory } from './pickerPresentation'

  let {
    category = 'other',
    size = 20,
  }: {
    category?: PickerCategory
    size?: number
  } = $props()
</script>

<svg
  viewBox="0 0 24 24"
  width={size}
  height={size}
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  class="extractor-picker-icon"
  data-category={category}
>
  {#if category === 'video'}
    <!-- Video: play triangle + waveform -->
    <path d="M4 7 L4 17 L11 12 Z" />
    <line x1="15" y1="10" x2="15" y2="14" />
    <line x1="18" y1="8" x2="18" y2="16" />
    <line x1="21" y1="10" x2="21" y2="14" />
  {:else if category === 'audio'}
    <!-- Audio: musical note -->
    <path d="M9 18V5l12-2v13" />
    <circle cx="6" cy="18" r="3" />
    <circle cx="18" cy="16" r="3" />
  {:else if category === 'image'}
    <!-- Image: picture frame with sun and mountain -->
    <rect x="3" y="3" width="18" height="18" rx="2" ry="2" />
    <circle cx="8.5" cy="8.5" r="1.5" />
    <polyline points="21 15 16 10 5 21" />
  {:else if category === 'archive'}
    <!-- Archive: zip container -->
    <path d="M11 3 H7 a2 2 0 0 0 -2 2 v14 a2 2 0 0 0 2 2 h10 a2 2 0 0 0 2 -2 V5 a2 2 0 0 0 -2 -2 h-4" />
    <line x1="12" y1="3" x2="12" y2="8" />
    <rect x="10.5" y="8" width="3" height="3" rx="0.5" />
    <line x1="12" y1="11" x2="12" y2="21" />
  {:else if category === 'document'}
    <!-- Document: folded page with lines -->
    <path d="M6 3 H14 L18 7 V19 a2 2 0 0 1 -2 2 H6 a2 2 0 0 1 -2 -2 V5 a2 2 0 0 1 2 -2" />
    <path d="M14 3 V7 H18" />
    <line x1="9" y1="11" x2="14" y2="11" />
    <line x1="9" y1="14" x2="16" y2="14" />
    <line x1="9" y1="17" x2="13" y2="17" />
  {:else}
    <!-- Other / Default: three-signal carrier -->
    <rect x="4" y="11" width="3" height="7" rx="1.5" />
    <rect x="10.5" y="7" width="3" height="11" rx="1.5" />
    <rect x="17" y="11" width="3" height="7" rx="1.5" />
  {/if}
</svg>
