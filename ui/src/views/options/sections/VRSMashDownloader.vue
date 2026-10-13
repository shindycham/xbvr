<template>
  <div class="container">
    <b-loading :is-full-page="false" :active.sync="isLoading" />
    <div class="content">
      <h3>{{ $t('VRSmash Downloader') }}</h3>
      <hr />

      <p>
        {{ $t('Download full-length videos from vrsmash.com for scenes already in your library.') }}
      </p>

      <b-field>
        <b-switch v-model="settings.enabled" type="is-info">
          {{ $t('Enable VRSmash downloads') }}
        </b-switch>
      </b-field>

      <b-field :label="$t('Download folder')"
        :message="$t('Folder on this machine to save downloads. Leave empty to use the SLR download folder.')">
        <b-input v-model="settings.downloadDir" placeholder="/path/to/library/VRSmash" expanded></b-input>
      </b-field>

      <b-field :label="$t('Preferred codec')">
        <b-select v-model="settings.preferredCodec" expanded>
          <option value="h264">h264</option>
          <option value="h265">h265</option>
        </b-select>
      </b-field>

      <b-field :label="$t('Cookie config key')"
        :message="$t('The collector config (Options -> Advanced) holding your vrsmash.com cookies, including cf_clearance and atoken.')">
        <b-input v-model="settings.cookieConfigKey" placeholder="vrsmash.com-downloader" expanded></b-input>
      </b-field>

      <b-field>
        <b-switch v-model="settings.useFirefoxCookies" type="is-dark">
          {{ $t('Fall back to a local Firefox profile when no cookies are configured') }}
        </b-switch>
      </b-field>

      <hr />

      <h4 class="title is-6">{{ $t('Session') }}</h4>

      <b-field>
        <b-tag :type="tokenTagType">{{ tokenStatusText }}</b-tag>
      </b-field>

      <p class="is-size-7 has-text-grey">
        {{ $t('vrsmash access tokens last about 15 minutes; the refresh token lasts 24 hours and is renewed on every refresh. With automatic refresh on, xbvr keeps the session alive on its own (it refreshes at least every 6 hours while running).') }}
      </p>

      <b-field>
        <b-switch v-model="settings.autoRefreshToken" type="is-info">
          {{ $t('Keep the session alive automatically') }}
        </b-switch>
      </b-field>

      <b-field :label="$t('Refresh token (rtoken)')"
        :message="$t('Paste the rtoken cookie from vrsmash.com. Tip: log in from a private/incognito window and copy that rtoken, so xbvr gets its own session instead of rotating your main browser one.')">
        <b-input v-model="settings.refreshToken" password-reveal type="password" placeholder="rtoken" expanded></b-input>
      </b-field>

      <p class="is-size-7 has-text-grey" v-if="settings.autoRefreshToken && !settings.refreshToken">
        {{ $t('No refresh token set: xbvr will try to use the one from your configured cookies / Firefox profile. If both share it, whichever refreshes first logs the other out - use a separate login to avoid that.') }}
      </p>

      <b-field>
        <router-link to="./downloads">{{ $t('View all downloads') }} &rarr;</router-link>
      </b-field>

      <b-button type="is-primary" :loading="saving" @click="save">{{ $t('Save') }}</b-button>
    </div>
  </div>
</template>

<script>
import ky from 'ky'

export default {
  name: 'VRSMashDownloader',
  data () {
    return {
      isLoading: true,
      saving: false,
      cookieSource: 'none',
      tokenValid: false,
      tokenExpires: '',
      settings: {
        enabled: true,
        downloadDir: '',
        preferredCodec: 'h264',
        cookieConfigKey: 'vrsmash.com-downloader',
        useFirefoxCookies: true,
        autoRefreshToken: false,
        refreshToken: ''
      }
    }
  },
  computed: {
    cookieTagType () {
      switch (this.cookieSource) {
        case 'config': return 'is-success'
        case 'token': return 'is-success'
        case 'firefox': return 'is-info'
        default: return 'is-danger'
      }
    },
    cookieStatusText () {
      switch (this.cookieSource) {
        case 'config': return this.$t('Cookies found in the collector config')
        case 'token': return this.$t('Using automatic session refresh (refresh token)')
        case 'firefox': return this.$t('No configured cookies; a local Firefox profile will be used')
        default: return this.$t('No vrsmash.com session found - downloads will fail')
      }
    },
    tokenTagType () {
      if (this.tokenValid) { return 'is-success' }
      if (this.settings.autoRefreshToken && this.settings.refreshToken) { return 'is-warning' }
      return 'is-danger'
    },
    tokenStatusText () {
      if (this.tokenValid) {
        if (this.tokenExpires) {
          return this.$t('Session valid until') + ' ' + new Date(this.tokenExpires).toLocaleString()
        }
        return this.$t('Session valid')
      }
      if (this.settings.autoRefreshToken && this.settings.refreshToken) {
        return this.$t('Session expired - it will refresh automatically on the next use')
      }
      return this.$t('Session expired - open vrsmash.com in a browser, or enable automatic refresh')
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
        if (state.config && state.config.vrsmashDownloader) {
          this.settings = { ...this.settings, ...state.config.vrsmashDownloader }
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
        this.cookieSource = status.vrsmash_cookie_source
        this.tokenValid = !!status.vrsmash_token_valid
        this.tokenExpires = status.vrsmash_token_expires || ''
      } catch (e) {
        this.cookieSource = 'none'
      }
    },
    async save () {
      this.saving = true
      try {
        const saved = await ky.put('/api/options/vrsmash-downloader', { json: this.settings }).json()
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
