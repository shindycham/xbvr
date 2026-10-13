import ky from 'ky'

const ACTIVE = ['queued', 'resolving', 'downloading']

const state = {
  items: [],
  quotas: {},
  loading: false,
  error: ''
}

const getters = {
  active (state) {
    return state.items.filter(d => ACTIVE.includes(d.status))
  },
  activeCount (state, getters) {
    return getters.active.length
  }
}

const mutations = {
  setItems (state, items) {
    state.items = items || []
  },
  upsert (state, item) {
    if (!item || !item.id) { return }
    const i = state.items.findIndex(d => d.id === item.id)
    if (i === -1) {
      state.items.push(item)
    } else {
      state.items.splice(i, 1, item)
    }
  },
  setQuota (state, payload) {
    state.quotas = { ...state.quotas, [payload.provider]: payload.quota }
  },
  setError (state, error) {
    state.error = error || ''
  }
}

const actions = {
  async load ({ commit }) {
    try {
      const items = await ky.get('/api/download/list').json()
      commit('setItems', items)
      commit('setError', '')
    } catch (e) {
      commit('setError', e.message)
    }
  },
  async loadQuota ({ commit }, provider) {
    const force = typeof provider === 'object' ? provider.force : false
    const id = typeof provider === 'object' ? provider.provider : provider
    try {
      const quota = await ky.get('/api/download/quota?provider=' + encodeURIComponent(id) + (force ? '&refresh=true' : '')).json()
      commit('setQuota', { provider: id, quota })
    } catch (e) {
      commit('setQuota', { provider: id, quota: { provider: id, error: e.message } })
    }
  },
  async cancel ({ dispatch }, id) {
    await ky.post('/api/download/cancel', { json: { id } })
    await dispatch('load')
  },
  async remove ({ dispatch }, id) {
    await ky.post('/api/download/remove', { json: { id } })
    await dispatch('load')
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
