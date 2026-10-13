<template>
  <div class="modal-card" style="width: auto; min-width: 560px;">
    <header class="modal-card-head">
      <p class="modal-card-title">
        <b-icon pack="mdi" icon="magnet" size="is-small" style="margin-right:0.3em"/>
        {{ $t('Search releases') }}
      </p>
    </header>

    <section class="modal-card-body">
      <p style="margin-bottom:1em;">
        <strong>{{ scene.title }}</strong>
      </p>

      <!-- Query override + protocol filter. The server sorts best-first. -->
      <form @submit.prevent="search">
        <div class="columns is-mobile" style="margin-bottom:0.75em;">
          <div class="column">
            <b-input
              v-model="query"
              size="is-small"
              :placeholder="scene.title"
              :disabled="loading">
            </b-input>
          </div>
          <div class="column is-narrow">
            <b-select v-model="protocol" size="is-small" :disabled="loading">
              <option v-for="p in protocols" :key="p.value" :value="p.value">{{ p.label }}</option>
            </b-select>
          </div>
          <div class="column is-narrow">
            <b-button native-type="submit" type="is-info" size="is-small" :loading="loading">
              {{ $t('Search') }}
            </b-button>
          </div>
        </div>
      </form>

      <b-notification v-if="error" type="is-danger" :closable="false" has-icon>
        {{ error }}
      </b-notification>
      <b-notification v-if="startError" type="is-danger" :closable="false" has-icon>
        {{ startError }}
      </b-notification>

      <div v-if="loading" class="has-text-centered" style="padding:1.5em;">
        <b-loading :is-full-page="false" :active="true"></b-loading>
        <p style="margin-top:2.5em;">{{ $t('Searching for releases...') }}</p>
      </div>

      <div v-else-if="!error">
        <p v-if="queries.length" class="is-size-7 has-text-grey" style="margin-bottom:0.75em;">
          {{ $t('Searched for') }}:
          <span v-for="(q, i) in queries" :key="i"><code>{{ q }}</code><span v-if="i < queries.length - 1">, </span></span>
        </p>

        <p v-if="!candidates.length" class="has-text-centered" style="opacity:0.7;">
          {{ $t('No releases were found for this scene.') }}
        </p>

        <div v-for="(candidate, idx) in candidates" :key="idx" class="grab-candidate">
          <div class="is-flex" style="align-items:flex-start; gap:0.5em;">
            <div style="min-width:0; flex:1 1 auto;">
              <p class="grab-title" :title="candidate.title">{{ candidate.title }}</p>
              <p class="grab-meta">
                <span>{{ candidate.indexer_name }}</span>
                <span v-if="candidate.size"> &middot; {{ prettyBytes(candidate.size) }}</span>
                <span v-if="candidate.seeders >= 0"> &middot; {{ $t('Seeders') }}: {{ candidate.seeders }}</span>
                <span v-if="candidate.leechers >= 0"> &middot; {{ $t('Leechers') }}: {{ candidate.leechers }}</span>
                <span v-if="candidate.release && candidate.release.resolution"> &middot; {{ candidate.release.resolution }}p</span>
                <span v-if="candidate.release && candidate.release.codec"> &middot; {{ candidate.release.codec }}</span>
                <span v-if="performers(candidate.release)"> &middot; {{ performers(candidate.release) }}</span>
              </p>
            </div>
            <div class="is-flex" style="align-items:center; gap:0.5em; flex:0 0 auto;">
              <b-tag :type="candidate.match ? 'is-success' : 'is-light'">
                {{ candidate.match ? $t('Match') : candidate.score }}
              </b-tag>
              <b-button
                type="is-info"
                size="is-small"
                :disabled="!!starting || !canDownload(candidate)"
                :loading="starting === candidate.title"
                @click="start(candidate)">
                {{ $t('Download') }}
              </b-button>
            </div>
          </div>
        </div>
      </div>
    </section>

    <footer class="modal-card-foot">
      <b-button @click="$emit('close')">{{ $t('Close') }}</b-button>
    </footer>
  </div>
</template>

<script>
import ky from 'ky'
import prettyBytes from 'pretty-bytes'

export default {
  name: 'GrabSearchDialog',
  props: {
    scene: Object
  },
  data () {
    return {
      loading: true,
      error: '',
      startError: '',
      candidates: [],
      queries: [],
      query: '',
      protocol: '',
      starting: ''
    }
  },
  computed: {
    protocols () {
      return [
        { value: '', label: this.$t('Torrent and NZB') },
        { value: 'torrent', label: this.$t('Torrent') },
        { value: 'nzb', label: this.$t('NZB') }
      ]
    }
  },
  mounted () {
    this.load()
  },
  methods: {
    prettyBytes,
    async load () {
      this.loading = true
      this.error = ''
      this.startError = ''
      try {
        const searchParams = { 'scene-id': this.scene.id }
        if (this.protocol) { searchParams.protocol = this.protocol }
        if (this.query) { searchParams.q = this.query }
        const data = await ky.get('/api/grab/search', { searchParams }).json()
        // A failed search is still HTTP 200, carrying an error instead of results.
        this.candidates = data.candidates || []
        this.queries = data.queries || []
        this.error = data.error || ''
      } catch (e) {
        this.candidates = []
        this.queries = []
        this.error = await this.readError(e)
      } finally {
        this.loading = false
      }
    },
    search () {
      this.load()
    },
    canDownload (candidate) {
      // A candidate with no way to fetch it cannot be grabbed.
      return !!(candidate.magnet_url || candidate.download_url)
    },
    performers (release) {
      if (!release || !release.performers || !release.performers.length) { return '' }
      return release.performers.join(', ')
    },
    async start (candidate) {
      if (this.starting || !this.canDownload(candidate)) { return }
      this.startError = ''
      this.starting = candidate.title
      try {
        // The candidate is echoed back verbatim so the server can hand it to the client.
        const grab = await ky.post('/api/grab/start', {
          json: { scene_id: this.scene.id, candidate }
        }).json()
        this.$store.commit('slrDownloads/upsertGrab', grab)
        this.$store.dispatch('slrDownloads/loadGrabs')
        this.$emit('started', grab)
        this.$emit('close')
      } catch (e) {
        const body = await this.readErrorBody(e)
        this.startError = (body && body.error) || e.message
      } finally {
        this.starting = ''
      }
    },
    async readErrorBody (e) {
      if (e && e.response) {
        try {
          return await e.response.json()
        } catch {
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

<style scoped>
.grab-candidate {
  border: 1px solid #dbdbdb;
  border-radius: 0.25rem;
  padding: 0.5em;
  margin-bottom: 0.5em;
}

.grab-title {
  font-family: monospace;
  font-size: 0.85em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.grab-meta {
  font-size: 0.8em;
  opacity: 0.8;
}
</style>
