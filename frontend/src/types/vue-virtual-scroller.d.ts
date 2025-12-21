declare module 'vue-virtual-scroller' {
  import { DefineComponent } from 'vue'

  export interface RecycleScrollerProps {
    items: unknown[]
    itemSize?: number | null
    minItemSize?: number
    sizeField?: string
    typeField?: string
    keyField?: string
    direction?: 'vertical' | 'horizontal'
    buffer?: number
    pageMode?: boolean
    prerender?: number
    emitUpdate?: boolean
    listTag?: string
    itemTag?: string
    listClass?: string | object | unknown[]
    itemClass?: string | object | unknown[]
  }

  export interface DynamicScrollerProps extends Omit<RecycleScrollerProps, 'minItemSize'> {
    minItemSize: number
  }

  export interface DynamicScrollerItemProps {
    item: unknown
    active: boolean
    sizeDependencies?: unknown[]
    watchData?: boolean
    tag?: string
    emitResize?: boolean
  }

  export interface RecycleScrollerSlotProps {
    item: unknown
    index: number
    active: boolean
  }

  export const RecycleScroller: DefineComponent<
    RecycleScrollerProps,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    {
      default: (props: RecycleScrollerSlotProps) => unknown
    }
  >

  export const DynamicScroller: DefineComponent<
    DynamicScrollerProps,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    Record<string, never>,
    {
      default: (props: RecycleScrollerSlotProps) => unknown
    }
  >

  export const DynamicScrollerItem: DefineComponent<DynamicScrollerItemProps>
}
