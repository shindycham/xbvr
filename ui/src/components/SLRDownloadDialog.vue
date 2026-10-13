<template>
  <div class="modal-card" style="width: auto; min-width: 480px;">
    <header class="modal-card-head">
      <p class="modal-card-title">
        <b-icon pack="mdi" icon="download" size="is-small" style="margin-right:0.3em"/>
        {{ $t('Download video') }}
      </p>
    </header>

    <section class="modal-card-body">
      <p style="margin-bottom:1em;">
        <strong>{{ scene.title }}</strong>
      </p>

      <b-notification v-if="error" type="is-danger" :closable="false" has-icon>
        {{ error }}
      </b-notification>

      <!-- Active / finished download for this scene -->
      <div v-if="download" style="margin-bottom:1em;">
        <b-notification
          :type="statusType"
          :closable="false"
          has-icon>
          <p>
            <strong>{{ download.quality || (download.height > 0 ? download.height + 'p' : '') }} {{ download.codec }}</strong>
            <span v-if="download.status === 'queued'"> &mdash; {{ $t('Queued') }}</span>
            <span v-else-if="download.status === 'resolving'"> &mdash; {{ $t('Finding available qualities...') }}</span>
            <span v-else-if="download.status === 'downloading'"> &mdash; {{ $t('Downloading') }}</span>
            <span v-else-if="download.status === 'completed'"> &mdash; {{ $t('Completed') }}</span>
            <span v-else-if="download.status === 'cancelled'"> &mdash; {{ $t('Cancelled, partial file kept for resume') }}</span>
            <span v-else-if="download.status === 'failed'"> &mdash; {{ $t('Failed') }}</span>
          </p>
          <p v-if="download.error" style="font-size:0.85em;">{{ download.error }}</p>
          <p v-if="download.status === 'completed'" style="font-size:0.85em;">{{ download.destination }}</p>

          <b-progress
            v-if="isActive"
            :value="download.progress"
            :max="100"
            show-value
            format="percent">
          </b-progress>
          <p v-if="isActive" style="font-size:0.8em;margin-top:0.2em;">
            {{ prettyBytes(download.downloaded) }}<span v-if="download.total"> / {{ prettyBytes(download.total) }}</span>
            <span v-if="download.speed"> &middot; {{ prettyBytes(download.speed) }}/s</span>
            <span v-if="download.eta"> &middot; {{ $t('ETA') }} {{ formatEta(download.eta) }}</span>
          </p>
        </b-notification>
      </div>

      <div v-if="loading" class="has-text-centered" style="padding:1.5em;">
        <b-loading :is-full-page="false" :active="true"></b-loading>
        <p style="margin-top:2.5em;">{{ $t('Looking up available qualities...') }}</p>
      </div>

      <div v-else-if="!error && renditions.length">
        <p style="margin-bottom:0.5em;">{{ $t('Choose the quality to download') }}:</p>
        <div v-for="group in groupedRenditions" :key="group.codec" style="margin-bottom:0.75em;">
          <p style="font-size:0.8em;text-transform:uppercase;opacity:0.7;">{{ group.codec }}</p>
          <div class="buttons">
            <b-button
              v-for="r in group.items"
              :key="r.codec + r.quality + r.height"
              type="is-primary is-light"
              size="is-small"
              :disabled="isActive"
              @click="start(r)">
              {{ r.quality || (r.height + 'p') }}<span v-if="r.size"> ({{ prettyBytes(r.size) }})</span>
            </b-button>
          </div>
        </div>
      </div>
    </section>

    <footer class="modal-card-foot">
      <b-button v-if="isActive" type="is-danger is-light" @click="cancel">
        {{ $t('Cancel download') }}
      </b-button>
      <b-button @click="$emit('close')">{{ $t('Close') }}</b-button>
    </footer>
  </div>
</template>

<script>
import ky from 'ky'
import prettyBytes from 'pretty-bytes'

export default {
  name: 'SLRDownloadDialog',
  props: {
    scene: Object
  },
  data () {
    return {
      loading: true,
      error: '',
      renditions: [],
      download: null,
      pollTimer: null
    }
  },
  computed: {
    groupedRenditions () {
      const groups = {}
      this.renditions.forEach(r => {
        if (!groups[r.codec]) { groups[r.codec] = [] }
        groups[r.codec].push(r)
      })
      return Object.keys(groups).sort().map(codec => ({ codec, items: groups[codec] }))
    },
    isActive () {
      return this.download != null && ['resolving', 'queued', 'downloading'].includes(this.download.status)
    },
    statusType () {
      if (!this.download) { return 'is-info' }
      switch (this.download.status) {
        case 'completed': return 'is-success'
        case 'failed': return 'is-danger'
        case 'cancelled': return 'is-warning'
        default: return 'is-info'
      }
    }
  },
  mounted () {
    this.loadFormats()
    this.refreshDownloads()
    this.pollTimer = setInterval(this.refreshDownloads, 1000)
  },
  beforeDestroy () {
    if (this.pollTimer) { clearInterval(this.pollTimer) }
  },
  methods: {
    prettyBytes,
    formatEta (seconds) {
      const m = Math.floor(seconds / 60)
      const s = Math.round(seconds % 60)
      return `${m}m${String(s).padStart(2, '0')}s`
    },
    async loadFormats () {
      this.loading = true
      this.error = ''
      try {
        const data = await ky.get(`/api/download/formats/${this.scene.id}`).json()
        this.renditions = data.renditions || []
        if (!this.renditions.length) {
          this.error = this.$t('No downloadable qualities were found for this scene.')
        }
      } catch (e) {
        this.error = await this.readError(e)
      } finally {
        this.loading = false
      }
    },
    async start (rendition) {
      this.error = ''
      try {
        this.download = await ky.post('/api/download/start', {
          json: { scene_id: this.scene.id, quality: rendition.quality, codec: rendition.codec }
        }).json()
      } catch (e) {
        const body = await this.readErrorBody(e)
        if (body && body.download) { this.download = body.download }
        this.error = (body && body.error) || e.message
      }
    },
    async cancel () {
      if (!this.download) { return }
      try {
        await ky.post('/api/download/cancel', { json: { id: this.download.id } })
      } catch (e) {
        this.error = await this.readError(e)
      }
    },
    async refreshDownloads () {
      try {
        const list = await ky.get('/api/download/list').json()
        const forScene = (list || []).filter(d => d.scene_db_id === this.scene.id)
        if (forScene.length) {
          // Prefer an active download, otherwise show the most recent one.
          const active = forScene.find(d => ['resolving', 'queued', 'downloading'].includes(d.status))
          this.download = active || forScene[forScene.length - 1]
        }
      } catch (e) {
        // Progress polling is best-effort.
      }
    },
    async readErrorBody (e) {
      if (e && e.response) {
        try {
          return await e.response.json()
        } catch (inner) {
          return null
        }
      }
      return null
    },
    async readError (e) {
      const body = await this.readErrorBody(e)
      if (body && body.error) { return body.error }
      if (e && e.response) { return `${e.response.status} ${e.response.statusText}` }
      return e.message
    }
  }
}
</script>
