<template>
  <a class="button is-info is-outlined is-small"
    @click="openDialog"
    :title="$t('Download video')">
    <b-icon pack="mdi" icon="download" size="is-small"/>
  </a>
</template>

<script>
import SLRDownloadDialog from './SLRDownloadDialog'

export default {
  name: 'SLRDownloadButton',
  props: { item: Object },
  methods: {
    openDialog () {
      this.$buefy.modal.open({
        parent: this,
        component: SLRDownloadDialog,
        hasModalCard: true,
        trapFocus: true,
        canCancel: ['escape', 'outside'],
        props: { scene: this.item }
      })
    }
  }
}
</script>
