<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import { ApiError, api } from '../lib/api'
import { humanizeServerError } from '../lib/errors'
import { markAuthenticated } from '../stores/auth'
import { landingPath } from '../router'

const route = useRoute()
const router = useRouter()

const passphrase = ref('')
const error = ref('')
const busy = ref(false)
const input = ref<HTMLInputElement | null>(null)

function nextPath(): string {
  const next = route.query.next
  if (typeof next === 'string' && next.startsWith('/') && !next.startsWith('//')) return next
  return landingPath()
}

async function submit(): Promise<void> {
  if (busy.value) return
  if (passphrase.value === '') {
    error.value = 'Isi passphrase dulu.'
    input.value?.focus()
    return
  }

  busy.value = true
  error.value = ''
  try {
    await api.login(passphrase.value)
    markAuthenticated()
    await router.replace(nextPath())
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 401) {
      error.value = 'Passphrase-nya belum tepat. Coba lagi.'
    } else if (cause instanceof ApiError) {
      error.value = humanizeServerError(cause.message)
    } else {
      error.value = 'Iris tidak bisa dihubungi. Periksa koneksi lalu coba lagi.'
    }
    await nextTick()
    input.value?.focus()
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  await nextTick()
  input.value?.focus()
})
</script>

<template>
  <div class="relative flex min-h-dvh flex-col items-center justify-center overflow-hidden bg-bg px-5 py-12">
    <main class="w-full max-w-[22rem]">
      <div class="flex flex-col items-center text-center">
        <div class="relative">
          <div
            class="halo pointer-events-none absolute left-1/2 top-1/2 h-[236px] w-[236px] -translate-x-1/2 -translate-y-1/2 rounded-full opacity-80"
            aria-hidden="true"
          />
          <BrandMark :size="88" variant="full" class="relative" />
        </div>
        <h1 class="mt-5 text-[26px] font-semibold leading-none tracking-[-0.02em] text-ink">
          Iris
        </h1>
        <p class="mt-2 text-[13px] text-muted">your Hermes, in your own rooms.</p>
      </div>

      <form class="mt-10" novalidate @submit.prevent="submit">
        <label for="passphrase" class="mb-1.5 block text-[12.5px] font-medium text-muted">
          Passphrase
        </label>
        <input
          id="passphrase"
          ref="input"
          v-model="passphrase"
          type="password"
          name="passphrase"
          autocomplete="current-password"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          placeholder="Passphrase-mu"
          class="w-full rounded-xl border border-line bg-surface px-4 py-3.5 text-[16px] tracking-wide text-ink outline-none transition-colors placeholder:text-quiet/80 focus:border-orchid/70 disabled:opacity-70"
        />

        <p
          class="mt-2 min-h-[1.2rem] text-[13px] leading-snug text-orchid"
          role="alert"
          data-testid="login-error"
        >
          {{ error }}
        </p>

        <button
          type="submit"
          class="tap mt-2 flex w-full items-center justify-center rounded-xl bg-violet px-4 text-[14px] font-medium text-ink transition-colors hover:bg-violet/90 disabled:cursor-not-allowed disabled:opacity-60"
          data-testid="login-submit"
          :disabled="busy"
        >
          {{ busy ? 'Memeriksa…' : 'Masuk' }}
        </button>
      </form>

      <p class="mt-8 text-center text-[11.5px] leading-relaxed text-quiet">
        Iris hanya untuk satu orang. Tidak ada pendaftaran, tidak ada email — satu passphrase.
      </p>
    </main>
  </div>
</template>
