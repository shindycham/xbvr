<template>
  <a class="button is-info is-outlined is-small"
    @click="openDialog"
    :title="$t('Search releases')">
    <b-icon pack="mdi" icon="magnet" size="is-small"/>
  </a>
</template>

<script>
import GrabSearchDialog from './GrabSearchDialog'

export default {
  name: 'GrabSearchButton',
  props: { item: Object },
  methods: {
    openDialog () {
      this.$buefy.modal.open({
        parent: this,
        component: GrabSearchDialog,
        hasModalCard: true,
        trapFocus: true,
        canCancel: ['escape', 'outside'],
        props: { scene: this.item }
      })
    }
  }
}
</script>
