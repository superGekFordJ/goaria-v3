import { applyCapsuleEvent, INITIAL_CAPSULE_STATE, type CapsuleEvent, type CapsuleState } from './capsuleUiState'

class CapsuleView {
  state = $state<CapsuleState>(INITIAL_CAPSULE_STATE)
  effects = $state<'full' | 'reduced'>('full')
  onClick: () => void = () => {}
  onIgnore: () => void = () => {}

  apply(event: CapsuleEvent): CapsuleState {
    this.state = applyCapsuleEvent(this.state, event)
    return this.state
  }
}

export const capsuleView = new CapsuleView()
