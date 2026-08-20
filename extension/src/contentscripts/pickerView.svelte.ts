import {
  applyPickerEvent,
  INITIAL_PICKER_STATE,
  type PickerEvent,
  type PickerState,
} from './pickerUiState'
import type { ExtractorPickerSubmitMessage } from '../utils/messaging'

class PickerView {
  state = $state<PickerState>(INITIAL_PICKER_STATE)
  onSubmit: (payload: Omit<ExtractorPickerSubmitMessage, 'page_token'>) => void = () => {}
  onCancel: () => void = () => {}

  apply(event: PickerEvent): PickerState {
    this.state = applyPickerEvent(this.state, event)
    return this.state
  }
}

export const pickerView = new PickerView()
