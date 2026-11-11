import { reactive } from 'vue'
import { api, ApiError } from '../lib/api'

const state = reactive({
  checked: false,
  authenticated: false,
})

let inflight: Promise<boolean> | null = null

/** Ask the server once per app load whether the HttpOnly cookie is valid. */
export function checkAuth(force = false): Promise<boolean> {
  if (state.checked && !force) return Promise.resolve(state.authenticated)
  if (inflight) return inflight

  inflight = (async () => {
    try {
      await api.me()
      state.authenticated = true
    } catch (error) {
      state.authenticated = false
      // A 401 is a normal answer; anything else means Iris itself is down and
      // the login screen will say so when the person tries to sign in.
      if (!(error instanceof ApiError) && error instanceof Error) {
        state.checked = true
      }
    } finally {
      state.checked = true
      inflight = null
    }
    return state.authenticated
  })()

  return inflight
}

export function markAuthenticated(): void {
  state.authenticated = true
  state.checked = true
}

export function markSignedOut(): void {
  state.authenticated = false
  state.checked = true
}

export function authState(): { checked: boolean; authenticated: boolean } {
  return state
}
