import {
  applyDomPickerEvent,
  INITIAL_DOM_PICKER_STATE,
  type DomPickerEvent,
  type DomPickerState,
} from './domPickerUiState'
import type { DomSubmitMessage } from '../utils/messaging'

class DomPickerView {
  state = $state<DomPickerState>(INITIAL_DOM_PICKER_STATE)
  onSubmit: (payload: Omit<DomSubmitMessage, 'catalog_id'>) => void = () => {}
  onCancel: () => void = () => {}

  apply(event: DomPickerEvent): DomPickerState {
    this.state = applyDomPickerEvent(this.state, event)
    return this.state
  }
}

export const domPickerView = new DomPickerView()
