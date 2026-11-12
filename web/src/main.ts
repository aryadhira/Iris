import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './style.css'

createApp(App).use(router).mount('#app')

// Web Push needs the installed PWA. Registration is harmless in a plain tab;
// the server only pushes to a device that explicitly subscribed.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {
      // A missing service worker must never break the chat shell.
    })
  })
}
