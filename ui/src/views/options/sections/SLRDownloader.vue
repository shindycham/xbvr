<template>
  <div class="container">
    <b-loading :is-full-page="false" :active.sync="isLoading" />
    <div class="content">
      <h3>{{ $t('SLR Downloader') }}</h3>
      <hr />

      <p>
        {{ $t('Download full-length videos from sexlikereal.com for scenes already in your library.') }}
        {{ $t('Scenes scraped from SexLikeReal show a download button in the scene list.') }}
      </p>

      <b-field>
        <b-switch v-model="settings.enabled" type="is-info">
          {{ $t('Enable SLR downloads') }}
        </b-switch>
      </b-field>

      <b-field :label="$t('Download folder')"
        :message="$t('Folder on this machine to save downloads. Put it inside a scanned library volume so xbvr links the finished file automatically.')">
        <b-input v-model="settings.downloadDir" placeholder="/path/to/library/SLR" expanded></b-input>
      </b-field>

      <b-field :label="$t('Preferred codec')">
        <b-select v-model="settings.preferredCodec" expanded>
          <option value="h264">h264</option>
          <option value="h265">h265</option>
        </b-select>
      </b-field>

      <b-field :label="$t('Cookie config key')"
        :message="$t('The collector config (Options -> Advanced) that holds your sexlikereal.com cookies. The refresh_jwt cookie is the one that matters.')">
        <b-input v-model="settings.cookieConfigKey" placeholder="sexlikereal.com-downloader" expanded></b-input>
      </b-field>

      <b-field :message="cookieMessage">
        <b-switch v-model="settings.useFirefoxCookies" type="is-dark">
          {{ $t('Fall back to a local Firefox profile when no cookies are configured') }}
        </b-switch>
      </b-field>

      <b-field>
        <b-switch v-model="settings.rescanAfterDownload" type="is-dark">
          {{ $t('Rescan volumes after a download to link the file') }}
        </b-switch>
      </b-field>

      <b-field :label="$t('Simultaneous downloads')"
        :message="$t('How many downloads run at once. Additional downloads wait in the queue shown on the Downloads page.')">
        <b-input v-model.number="settings.concurrentDownloads" type="number" min="1" max="10" expanded></b-input>
      </b-field>

      <b-field>
        <b-tag :type="cookieTagType">{{ cookieStatusText }}</b-tag>
      </b-field>

      <b-field>
        <router-link to="./downloads">{{ $t('View all downloads and remaining quota') }} &rarr;</router-link>
      </b-field>

      <b-button type="is-primary" :loading="saving" @click="save">{{ $t('Save') }}</b-button>
    </div>
  </div>
</template>

<script>
import ky from 'ky'

export default {
  name: 'SLRDownloader',
  data () {
    return {
      isLoading: true,
      saving: false,
      cookieSource: 'none',
      settings: {
        enabled: true,
        downloadDir: '',
        preferredCodec: 'h264',
        cookieConfigKey: 'sexlikereal.com-downloader',
        useFirefoxCookies: true,
        concurrentDownloads: 1,
        rescanAfterDownload: true
      }
    }
  },
  computed: {
    cookieTagType () {
      switch (this.cookieSource) {
        case 'config': return 'is-success'
        case 'firefox': return 'is-info'
        default: return 'is-danger'
      }
    },
    cookieStatusText () {
      switch (this.cookieSource) {
        case 'config': return this.$t('Cookies found in the collector config')
        case 'firefox': return this.$t('No configured cookies; a local Firefox profile will be used')
        default: return this.$t('No sexlikereal.com cookies found - downloads will fail')
      }
    },
    cookieMessage () {
      return this.$t('Cookies are read from the collector config first; the Firefox profile is only used when that is empty.')
    }
  },
  mounted () {
    this.load()
  },
  methods: {
    async load () {
      this.isLoading = true
      try {
        const state = await ky.get('/api/options/state').json()
        if (state.config && state.config.slrDownloader) {
          this.settings = { ...this.settings, ...state.config.slrDownloader }
        }
        await this.refreshStatus()
      } catch (e) {
        this.$buefy.toast.open({ message: e.message, type: 'is-danger' })
      } finally {
        this.isLoading = false
      }
    },
    async refreshStatus () {
      try {
        const status = await ky.get('/api/download/status').json()
        this.cookieSource = status.slr_cookie_source
      } catch (e) {
        this.cookieSource = 'none'
      }
    },
    async save () {
      this.saving = true
      try {
        const saved = await ky.put('/api/options/slr-downloader', { json: this.settings }).json()
        this.settings = { ...this.settings, ...saved }
        await this.refreshStatus()
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
