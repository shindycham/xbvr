<template>
  <div class="container">
    <b-loading :is-full-page="false" :active.sync="isLoading" />
    <div class="content">
      <h3>{{ $t('Torrent & NZB') }}</h3>
      <hr />

      <p>
        {{ $t('Search your indexers for a scene and hand the release to an external download client, then import what it finishes into your library.') }}
      </p>

      <b-field>
        <b-switch v-model="settings.enabled" type="is-info">
          {{ $t('Enable torrent and NZB grabbing') }}
        </b-switch>
      </b-field>

      <h4 class="title is-6">{{ $t('Import settings') }}</h4>

      <b-field :label="$t('Import folder')"
        :message="$t('Folder on this machine where finished downloads are placed. Put it inside a scanned library volume so xbvr links the file automatically.')">
        <b-input v-model="settings.importDir" placeholder="/path/to/library/Downloads" expanded></b-input>
      </b-field>

      <b-field :label="$t('Import mode')"
        :message="$t('Hardlink places the file without duplicating it, so the client can keep seeding; it needs the client and the library on the same filesystem. Copy duplicates the file, Move relocates the client copy (which stops seeding).')">
        <b-select v-model="settings.importMode" expanded>
          <option value="hardlink">{{ $t('Hardlink') }}</option>
          <option value="copy">{{ $t('Copy') }}</option>
          <option value="move">{{ $t('Move') }}</option>
        </b-select>
      </b-field>

      <b-field :message="$t('Delete the job from the download client once the file is imported. Safe with hardlinks; with Copy it leaves the imported file untouched.')">
        <b-switch v-model="settings.removeAfterImport" type="is-dark">
          {{ $t('Remove from the client after import') }}
        </b-switch>
      </b-field>

      <b-field :label="$t('Poll interval (seconds)')"
        :message="$t('How often active grabs are refreshed from the download clients. Values below 5 fall back to 15.')">
        <b-input v-model.number="settings.pollSeconds" type="number" min="5" expanded></b-input>
      </b-field>

      <b-field :label="$t('Minimum seeders')"
        :message="$t('Hide torrent results with fewer seeders than this.')">
        <b-input v-model.number="settings.minSeeders" type="number" min="0" expanded></b-input>
      </b-field>

      <b-field :label="$t('Preferred resolution')"
        :message="$t('Sort results of this height first, in pixels. 0 means no preference.')">
        <b-input v-model.number="settings.preferredResolution" type="number" min="0" expanded></b-input>
      </b-field>

      <h4 class="title is-6">{{ $t('Release name matching') }}</h4>

      <p class="is-size-7 has-text-grey">
        {{ $t('Torrent and NZB releases are named Sitename.dd.mm.yyyy.actor.releasegroup, which does not match your scene filenames. When this is on, a finished file is matched to a scene by studio, date and performers.') }}
      </p>

      <b-field>
        <b-switch v-model="settings.releaseMatchingEnabled" type="is-dark">
          {{ $t('Match release names to scenes') }}
        </b-switch>
      </b-field>

      <b-field :message="$t('When no scene matches yet, scrape the release studio so the scene can be created and matched.')">
        <b-switch v-model="settings.releaseMatchingAutoScrape" type="is-dark" :disabled="!settings.releaseMatchingEnabled">
          {{ $t('Scrape the studio when nothing matches') }}
        </b-switch>
      </b-field>

      <h4 class="title is-6">{{ $t('Indexers') }}</h4>

      <p class="is-size-7 has-text-grey">
        {{ $t('Torznab (torrent) or Newznab (Usenet) endpoints. Prowlarr and Jackett indexers work here.') }}
      </p>

      <div v-for="(row, i) in settings.indexers" :key="row.uid" class="box">
        <div class="is-flex" style="align-items:center;justify-content:space-between;gap:1em;">
          <p class="has-text-weight-bold">{{ row.name || $t('New indexer') }}</p>
          <b-button size="is-small" type="is-danger is-light" @click="removeIndexer(i)">{{ $t('Remove') }}</b-button>
        </div>

        <b-field :label="$t('Name')">
          <b-input v-model="row.name" placeholder="Prowlarr" expanded></b-input>
        </b-field>

        <b-field :label="$t('Type')">
          <b-select v-model="row.type" expanded>
            <option v-for="t in supportedIndexers" :key="t" :value="t">{{ t }}</option>
          </b-select>
        </b-field>

        <b-field :label="$t('URL')"
          :message="$t('The API root, with no search parameters. Prowlarr looks like http://host:9696/<indexer id>/api and Jackett like http://host:9117/api/v2.0/indexers/all/results/torznab/api. An indexer used directly is usually just https://host/api - a version segment such as /api/v2/ is not part of it.')">
          <b-input v-model="row.url" placeholder="http://127.0.0.1:9696/1/api" expanded></b-input>
        </b-field>

        <b-field :label="$t('API key')">
          <b-input v-model="row.apiKey" type="password" password-reveal expanded></b-input>
        </b-field>

        <b-field :label="$t('Categories')"
          :message="$t('Comma-separated newznab category ids, for example 6000, 6010.')">
          <b-input v-model="row.categoriesText" placeholder="6000" expanded></b-input>
        </b-field>

        <b-field>
          <b-switch v-model="row.enabled" type="is-dark">{{ $t('Enabled') }}</b-switch>
        </b-field>

        <div class="is-flex" style="align-items:center;gap:0.5em;flex-wrap:wrap;">
          <b-button size="is-small" :loading="indexerState(row).testing" @click="testIndexer(row)">{{ $t('Test') }}</b-button>
          <b-tag v-if="indexerState(row).ok === true" type="is-success">{{ $t('Connection OK') }}</b-tag>
          <b-tag v-else-if="indexerState(row).ok === false" type="is-danger">{{ indexerState(row).error || $t('Connection failed') }}</b-tag>
        </div>
      </div>

      <b-field>
        <b-button size="is-small" type="is-info is-light" @click="addIndexer">{{ $t('Add indexer') }}</b-button>
      </b-field>

      <h4 class="title is-6">{{ $t('Download clients') }}</h4>

      <p class="is-size-7 has-text-grey">
        {{ $t('qBittorrent and Transmission are torrent clients; a Usenet client is configured with kind NZB.') }}
      </p>

      <div v-for="(row, i) in settings.downloadClients" :key="row.uid" class="box">
        <div class="is-flex" style="align-items:center;justify-content:space-between;gap:1em;">
          <p class="has-text-weight-bold">{{ row.name || $t('New client') }}</p>
          <b-button size="is-small" type="is-danger is-light" @click="removeClient(i)">{{ $t('Remove') }}</b-button>
        </div>

        <b-field :label="$t('Name')">
          <b-input v-model="row.name" placeholder="qBittorrent" expanded></b-input>
        </b-field>

        <b-field :label="$t('Client')">
          <b-select v-model="row.client" expanded>
            <option v-for="c in supportedClients" :key="c" :value="c">{{ c }}</option>
          </b-select>
        </b-field>

        <b-field :label="$t('Kind')">
          <b-select v-model="row.kind" expanded>
            <option value="torrent">{{ $t('Torrent') }}</option>
            <option value="nzb">{{ $t('NZB') }}</option>
          </b-select>
        </b-field>

        <b-field :label="$t('URL')">
          <b-input v-model="row.url" placeholder="http://127.0.0.1:8080" expanded></b-input>
        </b-field>

        <b-field :label="$t('Username')">
          <b-input v-model="row.username" expanded></b-input>
        </b-field>

        <b-field :label="$t('Password')">
          <b-input v-model="row.password" type="password" password-reveal expanded></b-input>
        </b-field>

        <b-field :label="$t('API key')">
          <b-input v-model="row.apiKey" type="password" password-reveal expanded></b-input>
        </b-field>

        <b-field :label="$t('Category')">
          <b-input v-model="row.category" placeholder="xbvr" expanded></b-input>
        </b-field>

        <b-field :label="$t('Save path')"
          :message="$t('Optional client-side path for finished downloads. Leave empty to use the client default.')">
          <b-input v-model="row.savePath" expanded></b-input>
        </b-field>

        <b-field>
          <b-switch v-model="row.enabled" type="is-dark">{{ $t('Enabled') }}</b-switch>
        </b-field>

        <div class="is-flex" style="align-items:center;gap:0.5em;flex-wrap:wrap;">
          <b-button size="is-small" :loading="clientState(row).testing" @click="testClient(row)">{{ $t('Test') }}</b-button>
          <b-tag v-if="clientState(row).ok === true" type="is-success">{{ $t('Connection OK') }}</b-tag>
          <b-tag v-else-if="clientState(row).ok === false" type="is-danger">{{ clientState(row).error || $t('Connection failed') }}</b-tag>
        </div>
      </div>

      <b-field>
        <b-button size="is-small" type="is-info is-light" @click="addClient">{{ $t('Add client') }}</b-button>
      </b-field>

      <b-field>
        <router-link to="./downloads">{{ $t('View all downloads') }} &rarr;</router-link>
      </b-field>

      <b-button type="is-primary" :loading="saving" @click="save">{{ $t('Save') }}</b-button>
    </div>
  </div>
</template>

<script>
import ky from 'ky'

// Local-only key so a row keeps its test result while it is reordered or renamed.
let uidSeq = 0
const newUid = () => `row-${++uidSeq}`

function blankIndexer () {
  return { uid: newUid(), id: '', name: '', type: 'torznab', url: '', apiKey: '', categoriesText: '', enabled: true }
}

function blankClient () {
  return { uid: newUid(), id: '', name: '', client: 'qbittorrent', kind: 'torrent', url: '', username: '', password: '', apiKey: '', category: 'xbvr', savePath: '', enabled: true }
}

// Only the fields the API knows about are sent; the row's uid and the
// comma-separated categories text are UI-only.
function indexerPayload (row) {
  return {
    id: row.id || '',
    name: row.name || '',
    type: row.type || 'torznab',
    url: row.url || '',
    apiKey: row.apiKey || '',
    categories: (row.categoriesText || '').split(',').map(s => s.trim()).filter(s => s),
    enabled: !!row.enabled
  }
}

function clientPayload (row) {
  return {
    id: row.id || '',
    name: row.name || '',
    client: row.client || '',
    kind: row.kind || 'torrent',
    url: row.url || '',
    username: row.username || '',
    password: row.password || '',
    apiKey: row.apiKey || '',
    category: row.category || '',
    savePath: row.savePath || '',
    enabled: !!row.enabled
  }
}

function indexerRow (cfg) {
  return {
    ...blankIndexer(),
    id: cfg.id || '',
    name: cfg.name || '',
    type: cfg.type || 'torznab',
    url: cfg.url || '',
    apiKey: cfg.apiKey || '',
    categoriesText: (cfg.categories || []).join(', '),
    enabled: !!cfg.enabled
  }
}

function clientRow (cfg) {
  return {
    ...blankClient(),
    id: cfg.id || '',
    name: cfg.name || '',
    client: cfg.client || 'qbittorrent',
    kind: cfg.kind || 'torrent',
    url: cfg.url || '',
    username: cfg.username || '',
    password: cfg.password || '',
    apiKey: cfg.apiKey || '',
    category: cfg.category || '',
    savePath: cfg.savePath || '',
    enabled: !!cfg.enabled
  }
}

export default {
  name: 'TorrentNZB',
  data () {
    return {
      isLoading: true,
      saving: false,
      supportedIndexers: ['torznab', 'newznab'],
      supportedClients: ['qbittorrent', 'transmission'],
      indexerTest: {},
      clientTest: {},
      settings: {
        enabled: false,
        importDir: '',
        importMode: 'hardlink',
        removeAfterImport: false,
        pollSeconds: 15,
        minSeeders: 1,
        preferredResolution: 0,
        releaseMatchingEnabled: true,
        releaseMatchingAutoScrape: true,
        indexers: [],
        downloadClients: []
      }
    }
  },
  mounted () {
    this.load(true)
  },
  methods: {
    async load (showLoading) {
      if (showLoading) { this.isLoading = true }
      try {
        const status = await ky.get('/api/grab/status').json()
        if (status.supported_indexers && status.supported_indexers.length) {
          this.supportedIndexers = status.supported_indexers
        }
        if (status.supported_clients && status.supported_clients.length) {
          this.supportedClients = status.supported_clients
        }
        this.settings = {
          ...this.settings,
          enabled: !!status.enabled,
          importDir: status.import_dir || '',
          importMode: status.import_mode || 'hardlink',
          removeAfterImport: !!status.remove_after_import,
          pollSeconds: status.poll_seconds || 15,
          minSeeders: status.min_seeders || 0,
          preferredResolution: status.preferred_resolution || 0,
          releaseMatchingEnabled: status.release_matching_enabled !== false,
          releaseMatchingAutoScrape: status.release_matching_auto_scrape !== false,
          indexers: (status.indexers || []).map(indexerRow),
          downloadClients: (status.download_clients || []).map(clientRow)
        }
      } catch (e) {
        this.$buefy.toast.open({ message: e.message, type: 'is-danger' })
      } finally {
        this.isLoading = false
      }
    },
    indexerState (row) { return this.indexerTest[row.uid] || {} },
    clientState (row) { return this.clientTest[row.uid] || {} },
    addIndexer () { this.settings.indexers.push(blankIndexer()) },
    removeIndexer (i) { this.settings.indexers.splice(i, 1) },
    addClient () { this.settings.downloadClients.push(blankClient()) },
    removeClient (i) { this.settings.downloadClients.splice(i, 1) },
    async testIndexer (row) {
      this.$set(this.indexerTest, row.uid, { testing: true })
      try {
        const result = await ky.post('/api/grab/test-indexer', { json: indexerPayload(row) }).json()
        this.$set(this.indexerTest, row.uid, { testing: false, ok: !!result.ok, error: result.error || '' })
      } catch (e) {
        this.$set(this.indexerTest, row.uid, { testing: false, ok: false, error: e.message })
      }
    },
    async testClient (row) {
      this.$set(this.clientTest, row.uid, { testing: true })
      try {
        const result = await ky.post('/api/grab/test-client', { json: clientPayload(row) }).json()
        this.$set(this.clientTest, row.uid, { testing: false, ok: !!result.ok, error: result.error || '' })
      } catch (e) {
        this.$set(this.clientTest, row.uid, { testing: false, ok: false, error: e.message })
      }
    },
    async save () {
      this.saving = true
      try {
        const payload = {
          enabled: !!this.settings.enabled,
          importDir: this.settings.importDir,
          importMode: this.settings.importMode,
          removeAfterImport: !!this.settings.removeAfterImport,
          pollSeconds: this.settings.pollSeconds,
          minSeeders: this.settings.minSeeders,
          preferredResolution: this.settings.preferredResolution,
          indexers: this.settings.indexers.map(indexerPayload),
          downloadClients: this.settings.downloadClients.map(clientPayload),
          releaseMatchingEnabled: !!this.settings.releaseMatchingEnabled,
          releaseMatchingAutoScrape: !!this.settings.releaseMatchingAutoScrape
        }
        await ky.put('/api/options/torrent-nzb', { json: payload }).json()
        // Reload so server-side normalisation (ids, dropped empty rows) is shown.
        await this.load(false)
        this.$buefy.toast.open({ message: this.$t('Saved'), type: 'is-success' })
      } catch (e) {
        this.$buefy.toast.open({ message: e.message, type: 'is-danger' })
      } finally {
        this.saving = false
      }
    }
  }
}
</script>
