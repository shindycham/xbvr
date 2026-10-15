<template>
  <div class="container is-fluid">
    <div class="columns">
      <div class="column">
        <h3 class="title is-5">{{ $t('Duplicate scenes') }}</h3>
        <p class="is-size-7 has-text-grey">
          {{ $t('The same scene can appear once per source. Confirm the copies that are the same scene and pick the one to keep.') }}
        </p>
      </div>
      <div class="column has-text-right">
        <b-button
          size="is-small"
          icon-left="refresh"
          :loading="loadingCandidates"
          :disabled="loadingCandidates"
          @click="loadCandidates">
          {{ $t('Rescan') }}
        </b-button>
      </div>
    </div>

    <b-notification v-if="error" type="is-danger" :closable="false" has-icon>{{ error }}</b-notification>

    <b-loading :is-full-page="false" :active.sync="loadingCandidates" />

    <!-- Candidate groups -->
    <h4 v-if="candidates.length" class="title is-6">
      {{ $t('Candidates') }}
      <span v-if="visibleGroups.length" class="is-size-7 has-text-grey">{{ visibleGroups.length }}</span>
    </h4>

    <div v-for="group in visibleGroups" :key="group.uid" class="box">
      <div class="is-flex" style="align-items:center;justify-content:space-between;flex-wrap:wrap;gap:0.5em;">
        <p class="title is-6" style="margin-bottom:0">{{ group.title || group.key }}</p>
        <p>
          <b-tag v-for="reason in (group.reasons || [])" :key="reason" type="is-light" style="margin-left:0.25em">{{ reason }}</b-tag>
          <b-tag type="is-info" style="margin-left:0.25em">{{ $t('Score') }} {{ group.score }}</b-tag>
        </p>
      </div>

      <p class="is-size-7 has-text-grey" style="margin-top:0.25em">
        {{ sceneCountLabel(group) }}
      </p>

      <b-notification v-if="isHandled(group)" type="is-warning" :closable="false" style="margin-top:0.5em">
        {{ $t('Already handled: one of these copies is already merged into another scene, so there is nothing to merge here.') }}
      </b-notification>

      <div class="columns is-multiline" style="margin-top:0.25em">
        <div v-for="scene in (group.scenes || [])" :key="scene.id" class="column is-half">
          <div class="box" :style="sceneBoxStyle(group, scene)" style="height:100%;margin-bottom:0.75em;">
            <b-radio
              v-model="selection[group.uid]"
              :name="'duplicate-' + group.uid"
              :native-value="scene.id"
              :disabled="isHandled(group)">
              <span class="is-size-7">{{ $t('Keep this copy') }}</span>
            </b-radio>

            <div style="margin-top:0.5em">
              <img
                v-if="scene.cover_url"
                :src="imageURL(scene.cover_url)"
                :alt="scene.title"
                style="width:100%;height:120px;object-fit:cover;border-radius:4px;" />
              <div
                v-else
                class="has-background-grey-lighter"
                style="width:100%;height:120px;display:flex;align-items:center;justify-content:center;border-radius:4px;">
                <b-icon pack="mdi" icon="image-off-outline" size="is-large" class="has-text-grey" />
              </div>

              <p style="margin-top:0.4em;">
                <strong>{{ scene.studio || scene.site || scene.scene_id }}</strong>
                <b-tag v-if="scene.is_hidden" type="is-dark" style="margin-left:0.25em">{{ $t('Hidden') }}</b-tag>
              </p>
              <p class="is-size-7 has-text-grey">
                <span v-if="scene.site">{{ scene.site }}</span>
                <span v-if="scene.site && scene.scraper_id"> &middot; </span>
                <span v-if="scene.scraper_id">{{ scene.scraper_id }}</span>
                <span v-if="!scene.site && !scene.scraper_id">{{ scene.scene_id }}</span>
              </p>
              <p class="is-size-7 has-text-grey">
                <span v-if="formatDate(scene.release_date)">{{ formatDate(scene.release_date) }}</span>
                <span v-if="formatDate(scene.release_date) && formatDuration(scene.duration)"> &middot; </span>
                <span v-if="formatDuration(scene.duration)">{{ formatDuration(scene.duration) }}</span>
              </p>
              <p v-if="castText(scene)" class="is-size-7 has-text-grey">
                {{ $t('Cast') }}: {{ castText(scene) }}
              </p>

              <!-- Which copy actually has media or a funscript is the whole point of the review. -->
              <p style="margin-top:0.4em;">
                <b-tag :type="(scene.file_count || 0) > 0 ? 'is-success' : 'is-danger'" style="margin-right:0.25em">
                  {{ fileLabel(scene) }}
                </b-tag>
                <b-tag :type="(scene.script_count || 0) > 0 ? 'is-info' : 'is-light'">
                  {{ scriptLabel(scene) }}
                </b-tag>
              </p>
            </div>
          </div>
        </div>
      </div>

      <div class="is-flex" style="align-items:center;justify-content:flex-end;flex-wrap:wrap;gap:0.5em;">
        <b-button
          size="is-small"
          icon-left="eye-off-outline"
          :disabled="merging[group.uid]"
          :title="$t('Hidden for this session only. Rescan to see it again.')"
          @click="notDuplicate(group)">
          {{ $t('Not a duplicate') }}
        </b-button>
        <b-button
          v-if="!isHandled(group)"
          size="is-small"
          type="is-primary"
          icon-left="merge"
          :loading="merging[group.uid]"
          :disabled="merging[group.uid] || !canMerge(group)"
          @click="merge(group)">
          {{ $t('Merge into the selected scene') }}
        </b-button>
      </div>
    </div>

    <!-- Friendly empty state -->
    <div v-if="!loadingCandidates && !candidates.length" class="box">
      <p>{{ $t('No duplicate scenes found.') }}</p>
      <p class="is-size-7 has-text-grey" style="margin-top:0.4em;">
        {{ $t('xbvr compares titles, release dates, cast and duration, so scenes that differ in all of those will not be offered.') }}
      </p>
    </div>

    <p v-else-if="!loadingCandidates && candidates.length && !visibleGroups.length" class="has-text-grey">
      {{ $t('Every group has been dismissed for this session. Rescan to review them again.') }}
    </p>

    <!-- Existing merges -->
    <h4 class="title is-6" style="margin-top:1.5em">{{ $t('Merged scenes') }}</h4>
    <p class="is-size-7 has-text-grey">
      {{ $t('A merge hides the duplicate rather than deleting it, so nothing is lost. Undo returns it to the library.') }}
    </p>

    <b-loading :is-full-page="false" :active.sync="loadingMerged" />

    <p v-if="!loadingMerged && !merged.length" class="has-text-grey">{{ $t('Nothing has been merged yet.') }}</p>

    <div v-for="m in merged" :key="m.duplicate_id" class="box" style="padding:0.9em 1em;">
      <div class="is-flex" style="align-items:center;justify-content:space-between;flex-wrap:wrap;gap:1em;">
        <div style="min-width:0;flex:1">
          <p>
            <strong>{{ m.duplicate_title || m.duplicate_scene_id }}</strong>
            {{ $t('merged into') }}
            <strong>{{ m.primary_title || m.primary_scene_id }}</strong>
          </p>
          <p class="is-size-7 has-text-grey">
            <span v-if="formatDateTime(m.created_at)">{{ formatDateTime(m.created_at) }} &middot; </span>
            {{ m.duplicate_scene_id }}
          </p>
        </div>
        <b-button
          size="is-small"
          icon-left="undo"
          :loading="unmerging[m.duplicate_id]"
          :disabled="unmerging[m.duplicate_id]"
          :title="$t('The duplicate reappears in the library.')"
          @click="unmerge(m)">
          {{ $t('Undo') }}
        </b-button>
      </div>
    </div>
  </div>
</template>

<script>
import ky from 'ky'

export default {
  name: 'Duplicates',
  data () {
    return {
      candidates: [],
      merged: [],
      dismissed: [],
      selection: {},
      merging: {},
      unmerging: {},
      loadingCandidates: false,
      loadingMerged: false,
      error: ''
    }
  },
  computed: {
    visibleGroups () {
      return this.candidates.filter(group => !this.dismissed.includes(group.uid))
    }
  },
  mounted () {
    this.loadCandidates()
    this.loadMerged()
  },
  methods: {
    async loadCandidates () {
      this.loadingCandidates = true
      this.error = ''
      try {
        // The scan can take a moment on a large library, so let it run.
        const data = await ky.get('/api/duplicates/candidates', {
          timeout: false,
          searchParams: { limit: 100 }
        }).json()
        const groups = Array.isArray(data) ? data : []
        const selection = {}
        this.candidates = groups.map((group, index) => {
          const candidate = { ...group, uid: 'g' + index }
          const primary = this.defaultPrimary(candidate)
          selection[candidate.uid] = primary ? primary.id : 0
          return candidate
        })
        this.selection = selection
        this.dismissed = []
      } catch (e) {
        this.error = await this.readError(e)
      } finally {
        this.loadingCandidates = false
      }
    },
    async loadMerged () {
      this.loadingMerged = true
      try {
        const data = await ky.get('/api/duplicates/merged', { timeout: false }).json()
        this.merged = Array.isArray(data) ? data : []
      } catch (e) {
        this.error = await this.readError(e)
      } finally {
        this.loadingMerged = false
      }
    },
    // Default to a copy that actually has media: keeping a scene with no file
    // would leave the merged scene unplayable.
    defaultPrimary (group) {
      const scenes = group.scenes || []
      return scenes.find(scene => (scene.file_count || 0) > 0) || scenes[0] || null
    },
    selectedPrimary (group) {
      const id = this.selection[group.uid]
      return (group.scenes || []).find(scene => scene.id === id) || null
    },
    duplicatesFor (group) {
      const primary = this.selectedPrimary(group)
      if (!primary) { return [] }
      return (group.scenes || []).filter(scene => scene.id !== primary.id)
    },
    canMerge (group) {
      return !this.isHandled(group) && !!this.selectedPrimary(group) && this.duplicatesFor(group).length > 0
    },
    isHandled (group) {
      return (group.scenes || []).some(scene => !!scene.merged_into)
    },
    notDuplicate (group) {
      if (!this.dismissed.includes(group.uid)) {
        this.dismissed.push(group.uid)
      }
    },
    async merge (group) {
      const primary = this.selectedPrimary(group)
      const duplicates = this.duplicatesFor(group).map(scene => scene.id)
      if (!primary || !duplicates.length) { return }

      this.$set(this.merging, group.uid, true)
      this.error = ''
      try {
        await ky.post('/api/duplicates/merge', {
          json: { primary_scene_id: primary.id, duplicate_scene_ids: duplicates }
        }).json()
        this.candidates = this.candidates.filter(candidate => candidate.uid !== group.uid)
        await this.loadMerged()
      } catch (e) {
        const body = await this.readErrorBody(e)
        if (body && body.merged > 0) {
          // A partial merge is not a total failure: report both what happened
          // and what went wrong, then resync so the list matches the server.
          this.error = this.$t('Partially merged') + ': ' + body.merged + ' ' +
            this.$t('scenes merged before an error') + ': ' + (body.error || this.statusText(e))
          await this.loadCandidates()
          await this.loadMerged()
        } else {
          this.error = (body && body.error) || this.statusText(e)
        }
      } finally {
        this.$set(this.merging, group.uid, false)
      }
    },
    async unmerge (m) {
      this.$set(this.unmerging, m.duplicate_id, true)
      this.error = ''
      try {
        await ky.post('/api/duplicates/unmerge', {
          json: { duplicate_scene_id: m.duplicate_id }
        }).json()
        await this.loadCandidates()
        await this.loadMerged()
      } catch (e) {
        this.error = await this.readError(e)
      } finally {
        this.$set(this.unmerging, m.duplicate_id, false)
      }
    },
    async readErrorBody (e) {
      if (e && e.response) {
        try {
          // Read as text first: restful writes some errors as plain text, and
          // reading as JSON would consume the body before the fallback.
          const text = await e.response.text()
          if (text) {
            try {
              return JSON.parse(text)
            } catch (inner) {
              return { error: text }
            }
          }
        } catch (inner) {
          return null
        }
      }
      return null
    },
    statusText (e) {
      if (e && e.response) { return `${e.response.status} ${e.response.statusText}` }
      return (e && e.message) || 'Request failed'
    },
    async readError (e) {
      const body = await this.readErrorBody(e)
      if (body && body.error) { return body.error }
      return this.statusText(e)
    },
    sceneCountLabel (group) {
      const n = (group.scenes || []).length
      return n + ' ' + this.$t(n === 1 ? 'copy' : 'copies') + ' ' + this.$t('of this scene')
    },
    fileLabel (scene) {
      const n = scene.file_count || 0
      if (n === 0) { return this.$t('no file') }
      return n + ' ' + this.$t(n === 1 ? 'file' : 'files')
    },
    scriptLabel (scene) {
      const n = scene.script_count || 0
      if (n === 0) { return this.$t('no script') }
      return n + ' ' + this.$t(n === 1 ? 'script' : 'scripts')
    },
    castText (scene) {
      if (!scene || !Array.isArray(scene.cast) || !scene.cast.length) { return '' }
      return scene.cast.join(', ')
    },
    // A scene's duration is stored in minutes (SceneCard renders it as "<n>m").
    formatDuration (minutes) {
      const total = Number(minutes)
      if (!total || total <= 0) { return '' }
      const hours = Math.floor(total / 60)
      const mins = Math.round(total % 60)
      if (hours > 0) { return `${hours}h ${mins}m` }
      return `${mins}m`
    },
    formatDate (value) {
      if (!value || String(value).startsWith('0001')) { return '' }
      const d = new Date(value)
      if (isNaN(d.getTime())) { return '' }
      return d.toLocaleDateString()
    },
    formatDateTime (value) {
      if (!value || String(value).startsWith('0001')) { return '' }
      const d = new Date(value)
      if (isNaN(d.getTime())) { return '' }
      return d.toLocaleString()
    },
    imageURL (url) {
      if (!url) { return '' }
      try {
        if (url.startsWith('http')) {
          if (url.indexOf('%') === -1) {
            return '/img/200x/' + encodeURI(url)
          }
          return '/img/200x/' + encodeURI(decodeURI(url))
        }
      } catch (e) {
        // show the raw URL rather than throwing
      }
      return url
    },
    sceneBoxStyle (group, scene) {
      if (this.selection[group.uid] === scene.id) {
        return { border: '2px solid #7957d5' }
      }
      return { border: '2px solid transparent' }
    }
  }
}
</script>
