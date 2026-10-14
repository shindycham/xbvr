<template>
  <div class="container is-fluid">
    <div class="columns">
      <div class="column">
        <h3 class="title is-5">{{ $t('Downloads') }}</h3>
      </div>
      <div class="column has-text-right">
        <b-button size="is-small" icon-left="refresh" @click="refreshAll">{{ $t('Refresh') }}</b-button>
      </div>
    </div>

    <b-loading :is-full-page="false" :active.sync="isLoading" />

    <!-- Provider quotas -->
    <div class="columns">
      <div class="column" v-for="card in quotaCards" :key="card.id">
        <div class="box">
          <p class="heading">{{ card.name }}</p>

          <b-notification v-if="card.quota.error" type="is-light" :closable="false">
            {{ card.quota.error }}
          </b-notification>

          <template v-else>
            <div class="is-flex" style="align-items:baseline;justify-content:space-between">
              <p class="title is-3" style="margin-bottom:0">
                {{ card.quota.scene_downloads.remaining }}
                <span class="subtitle is-6">/ {{ card.quota.scene_downloads.limit }} {{ $t('left') }}</span>
              </p>
              <p class="is-size-7 has-text-grey">
                {{ $t('resets') }} {{ formatReset(card.quota.scene_downloads.resets_at) }}
              </p>
            </div>
            <b-progress
              :value="card.quota.scene_downloads.used"
              :max="card.quota.scene_downloads.limit"
              :type="quotaType(card.quota.scene_downloads)"
              size="is-medium"
              show-value>
              {{ card.quota.scene_downloads.used }} {{ $t('used') }}
            </b-progress>
            <p v-if="card.quota.script_downloads && card.quota.script_downloads.limit > 0" class="is-size-7 has-text-grey">
              {{ $t('Scripts') }}: {{ card.quota.script_downloads.remaining }} / {{ card.quota.script_downloads.limit }} {{ $t('left') }}
              &middot; {{ $t('resets') }} {{ formatReset(card.quota.script_downloads.resets_at) }}
            </p>
          </template>
        </div>
      </div>
    </div>

    <!-- Torrents & NZBs -->
    <template v-if="grabs.length">
      <div class="columns" style="margin-bottom:0">
        <div class="column">
          <h4 class="title is-6">{{ $t('Torrents & NZBs') }}</h4>
        </div>
        <div class="column has-text-right">
          <b-button size="is-small" icon-left="refresh" @click="reloadGrabs">{{ $t('Refresh') }}</b-button>
        </div>
      </div>

      <div v-for="g in grabs" :key="g.id" class="box" style="padding:0.9em 1em;">
        <div class="is-flex" style="align-items:center;justify-content:space-between;gap:1em;">
          <div style="min-width:0;flex:1">
            <p style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">
              <strong>{{ g.release_title || g.title || g.scene_id }}</strong>
              <b-tag v-if="g.client_name" type="is-dark" style="margin-left:0.5em">{{ g.client_name }}</b-tag>
              <b-tag v-if="g.quality || g.resolution" style="margin-left:0.25em">{{ g.quality || (g.resolution + 'p') }} {{ g.codec }}</b-tag>
              <b-tag :type="statusType(g.status)" style="margin-left:0.25em">{{ statusLabel(g.status) }}</b-tag>
            </p>
            <p v-if="g.title && g.title !== g.release_title" class="is-size-7 has-text-grey">
              {{ g.title }}<span v-if="g.scene_id"> &middot; {{ g.scene_id }}</span>
            </p>

            <b-progress
              v-if="isActive(g)"
              :value="g.progress"
              :max="100"
              :type="g.status === 'queued' ? 'is-light' : 'is-info'"
              size="is-small"
              style="margin-top:0.4em">
            </b-progress>

            <p class="is-size-7 has-text-grey" style="margin-top:0.25em">
              <template v-if="isActive(g)">
                <span v-if="g.status === 'queued'">{{ $t('Queued') }}</span>
                <span v-else-if="g.status === 'importing'">{{ $t('Importing') }}</span>
                <template v-else>
                  {{ prettyBytes(g.downloaded) }}<span v-if="g.total"> / {{ prettyBytes(g.total) }}</span>
                  ({{ Math.round(g.progress) }}%)
                  <span v-if="g.speed"> &middot; {{ prettyBytes(g.speed) }}/s</span>
                  <span v-if="g.eta"> &middot; {{ $t('ETA') }} {{ formatEta(g.eta) }}</span>
                  <span v-if="showsSeeders(g)"> &middot; {{ g.seeders }} {{ $t('seeders') }}</span>
                </template>
              </template>
              <template v-else-if="g.status === 'completed'">
                {{ prettyBytes(g.total || g.size) }}<span v-if="g.destination"> &middot; {{ g.destination }}</span>
              </template>
              <template v-else-if="g.error">{{ g.error }}</template>
            </p>
          </div>

          <div style="white-space:nowrap;">
            <b-button v-if="isActive(g)" size="is-small" type="is-danger is-light" @click="cancelGrab(g)">{{ $t('Cancel') }}</b-button>
            <b-button v-else size="is-small" @click="removeGrab(g)"><b-icon pack="mdi" icon="close" size="is-small"/></b-button>
          </div>
        </div>
      </div>
    </template>

    <!-- All downloads -->
    <h4 class="title is-6">{{ $t('All downloads') }}</h4>

    <b-notification v-if="error" type="is-danger" :closable="false" has-icon>{{ error }}</b-notification>

    <p v-if="!items.length" class="has-text-grey">{{ $t('No downloads yet. Use the download button on a scene.') }}</p>

    <div v-for="d in orderedItems" :key="d.id" class="box" style="padding:0.9em 1em;">
      <div class="is-flex" style="align-items:center;justify-content:space-between;gap:1em;">
        <div style="min-width:0;flex:1">
          <p style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">
            <strong>{{ d.title || d.scene_id }}</strong>
            <b-tag v-if="d.provider_name" type="is-dark" style="margin-left:0.5em">{{ d.provider_name }}</b-tag>
            <b-tag v-if="d.quality || d.height" style="margin-left:0.25em">{{ d.quality || (d.height + 'p') }} {{ d.codec }}</b-tag>
            <b-tag :type="statusType(d.status)" style="margin-left:0.25em">{{ statusLabel(d.status) }}</b-tag>
          </p>

          <b-progress
            v-if="isActive(d)"
            :value="d.progress"
            :max="100"
            :type="d.status === 'queued' ? 'is-light' : 'is-info'"
            size="is-small"
            style="margin-top:0.4em">
          </b-progress>

          <p class="is-size-7 has-text-grey" style="margin-top:0.25em">
            <template v-if="isActive(d)">
              <span v-if="d.status === 'queued'">{{ $t('Queued') }}</span>
              <template v-else>
                {{ prettyBytes(d.downloaded) }}<span v-if="d.total"> / {{ prettyBytes(d.total) }}</span>
                ({{ Math.round(d.progress) }}%)
                <span v-if="d.speed"> &middot; {{ prettyBytes(d.speed) }}/s</span>
                <span v-if="d.eta"> &middot; {{ $t('ETA') }} {{ formatEta(d.eta) }}</span>
              </template>
            </template>
            <template v-else-if="d.status === 'completed'">
              {{ prettyBytes(d.total || d.downloaded) }} &middot; {{ d.destination }}
            </template>
            <template v-else-if="d.error">{{ d.error }}</template>
          </p>
        </div>

        <div style="white-space:nowrap;">
          <b-button v-if="isActive(d)" size="is-small" type="is-danger is-light" @click="cancel(d)">{{ $t('Cancel') }}</b-button>
          <b-button v-else size="is-small" @click="remove(d)"><b-icon pack="mdi" icon="close" size="is-small"/></b-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import prettyBytes from 'pretty-bytes'

const ACTIVE = ['queued', 'resolving', 'downloading', 'importing']
const PROVIDER_NAMES = { slr: 'SexLikeReal', vrsmash: 'VRSmash' }

export default {
  name: 'Downloads',
  data () {
    return {
      prettyBytes,
      timer: null,
      quotaTimer: null
    }
  },
  computed: {
    items () { return this.$store.state.slrDownloads.items },
    grabs () { return this.$store.state.slrDownloads.grabs || [] },
    quotas () { return this.$store.state.slrDownloads.quotas },
    error () { return this.$store.state.slrDownloads.error },
    isLoading () { return this.$store.state.slrDownloads.loading },
    quotaCards () {
      const ids = Object.keys(this.quotas)
      const ordered = ['slr', 'vrsmash'].filter(id => ids.includes(id)).concat(ids.filter(id => !['slr', 'vrsmash'].includes(id)))
      return ordered.map(id => ({
        id,
        name: PROVIDER_NAMES[id] || id,
        quota: this.quotas[id]
      }))
    },
    orderedItems () {
      const rank = { downloading: 0, resolving: 1, queued: 2, failed: 3, cancelled: 4, completed: 5 }
      return [...this.items].sort((a, b) => {
        const byStatus = (rank[a.status] || 9) - (rank[b.status] || 9)
        // Active work first, but never leave the order to chance when two
        // entries share a status: the list is polled once a second, and an
        // unstable comparison makes it look like the entries are swapping.
        return byStatus !== 0 ? byStatus : String(a.id).localeCompare(String(b.id))
      })
    }
  },
  mounted () {
    this.refreshAll()
    this.$store.dispatch('slrDownloads/loadGrabs')
    this.timer = setInterval(() => this.$store.dispatch('slrDownloads/load'), 1000)
    this.quotaTimer = setInterval(() => this.refreshQuotas(false), 60000)
  },
  beforeDestroy () {
    clearInterval(this.timer)
    clearInterval(this.quotaTimer)
  },
  methods: {
    refreshAll () {
      this.$store.dispatch('slrDownloads/load')
      this.reloadGrabs()
      this.refreshQuotas(true)
    },
    reloadGrabs () {
      this.$store.dispatch('slrDownloads/refreshGrabs')
    },
    refreshQuotas (force) {
      this.$store.dispatch('slrDownloads/loadQuota', { provider: 'slr', force })
      this.$store.dispatch('slrDownloads/loadQuota', { provider: 'vrsmash', force })
    },
    isActive (d) { return ACTIVE.includes(d.status) },
    statusType (status) {
      switch (status) {
        case 'completed': return 'is-success'
        case 'failed': return 'is-danger'
        case 'cancelled': return 'is-warning'
        case 'queued': return 'is-light'
        case 'importing': return 'is-info'
        default: return 'is-info'
      }
    },
    // Seeders describe a torrent swarm; Usenet has none, and -1 is how an
    // indexer that reports nothing is represented.
    showsSeeders (g) {
      return g.provider === 'torrent' && g.seeders >= 0
    },
    statusLabel (status) {
      return this.$t(status.charAt(0).toUpperCase() + status.slice(1))
    },
    quotaType (q) {
      if (!q || q.limit <= 0) { return 'is-light' }
      const usedPct = q.used / q.limit
      if (usedPct >= 1) { return 'is-danger' }
      if (usedPct >= 0.8) { return 'is-warning' }
      return 'is-success'
    },
    formatReset (value) {
      if (!value || value.startsWith('0001')) { return '—' }
      const d = new Date(value)
      if (isNaN(d.getTime())) { return '—' }
      return d.toLocaleString()
    },
    formatEta (seconds) {
      const m = Math.floor(seconds / 60)
      const s = Math.round(seconds % 60)
      return `${m}m${String(s).padStart(2, '0')}s`
    },
    async cancel (d) {
      try { await this.$store.dispatch('slrDownloads/cancel', d.id) } catch (e) { /* surfaced via load */ }
    },
    async remove (d) {
      try { await this.$store.dispatch('slrDownloads/remove', d.id) } catch (e) { /* ignore */ }
    },
    async cancelGrab (g) {
      try { await this.$store.dispatch('slrDownloads/cancelGrab', g.id) } catch (e) { /* surfaced via loadGrabs */ }
    },
    async removeGrab (g) {
      try { await this.$store.dispatch('slrDownloads/removeGrab', g.id) } catch (e) { /* ignore */ }
    }
  }
}
</script>
