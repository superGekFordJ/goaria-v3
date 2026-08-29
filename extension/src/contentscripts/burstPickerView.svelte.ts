import {
  applyBurstPickerEvent,
  INITIAL_BURST_PICKER_STATE,
  type BurstPickerEvent,
  type BurstPickerState,
} from './burstPickerUiState'
import type { BurstSubmitMessage } from '../utils/messaging'

class BurstPickerView {
  state = $state<BurstPickerState>(INITIAL_BURST_PICKER_STATE)
  onSubmit: (payload: Omit<BurstSubmitMessage, 'capture_id'>) => void = () => {}
  onCancel: () => void = () => {}

  apply(event: BurstPickerEvent): BurstPickerState {
    this.state = applyBurstPickerEvent(this.state, event)
    return this.state
  }
}

export const burstPickerView = new BurstPickerView()
