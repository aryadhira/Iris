<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { checkAuth } from '../stores/auth'
import { landingPath } from '../router'

const router = useRouter()

onMounted(async () => {
  // Normally the router guard has already redirected `/`; this is the
  // fallback for a guard that ran before the auth state was known.
  if (await checkAuth()) await router.replace(landingPath())
  else await router.replace({ name: 'login' })
})
</script>

<template>
  <div class="flex h-dvh items-center justify-center bg-bg">
    <p class="text-sm text-quiet">Membuka Iris…</p>
  </div>
</template>
