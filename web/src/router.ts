import { createRouter, createWebHistory } from 'vue-router'
import { checkAuth } from './stores/auth'
import { lastRoomId } from './stores/rooms'
import LoginView from './views/LoginView.vue'
import AppFrame from './views/AppFrame.vue'
import RoomsView from './views/RoomsView.vue'
import RoomView from './views/RoomView.vue'
import HomeRedirect from './views/HomeRedirect.vue'

/** Where a signed-in visitor belongs: the last room, else the room list. */
export function landingPath(): string {
  const last = lastRoomId()
  return last ? `/r/${last}` : '/rooms'
}

/** Only same-origin paths are accepted from ?next=. */
function safeNext(value: unknown): string | null {
  if (typeof value !== 'string') return null
  if (!value.startsWith('/') || value.startsWith('//')) return null
  return value
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginView },
    {
      path: '/',
      component: AppFrame,
      children: [
        { path: '', name: 'home', component: HomeRedirect },
        { path: 'rooms', name: 'rooms', component: RoomsView },
        { path: 'r/:id', name: 'room', component: RoomView },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const authenticated = await checkAuth()

  if (to.name === 'login') {
    if (!authenticated) return true
    return safeNext(to.query.next) ?? landingPath()
  }

  if (!authenticated) {
    return {
      name: 'login',
      query: to.fullPath === '/' ? {} : { next: to.fullPath },
    }
  }

  if (to.name === 'home') {
    return safeNext(to.query.next) ?? landingPath()
  }

  return true
})

router.afterEach((to) => {
  const titles: Record<string, string> = {
    login: 'Masuk — Iris',
    rooms: 'Rooms — Iris',
    room: 'Room — Iris',
  }
  document.title = titles[String(to.name)] ?? 'Iris'
})
