<script setup lang="ts">
import { Handle, Position } from '@vue-flow/core';

// Auxiliary left/right pins for Builder design nodes. Each side gets 3
// source-+-target pairs at 25/50/75%. The top/bottom semantic handles
// (tools / agents / orchestrators / input) stay untouched — this is purely
// additive so wires can enter/exit from the sides when that reads
// cleaner than going through the type-coloured bottom bar.
//
// Handle IDs: `s-left-1..3` / `t-left-1..3` / `s-right-1..3` / `t-right-1..3`.
const PIN_OFFSETS = ['25%', '50%', '75%'] as const;
</script>

<template>
  <template v-for="(off, i) in PIN_OFFSETS" :key="`bleft-${i}`">
    <Handle
      :id="`s-left-${i + 1}`"
      type="source"
      :position="Position.Left"
      :style="{ top: off }"
      class="builder-side-handle"
    />
    <Handle
      :id="`t-left-${i + 1}`"
      type="target"
      :position="Position.Left"
      :style="{ top: off }"
      class="builder-side-handle"
    />
  </template>
  <template v-for="(off, i) in PIN_OFFSETS" :key="`bright-${i}`">
    <Handle
      :id="`s-right-${i + 1}`"
      type="source"
      :position="Position.Right"
      :style="{ top: off }"
      class="builder-side-handle"
    />
    <Handle
      :id="`t-right-${i + 1}`"
      type="target"
      :position="Position.Right"
      :style="{ top: off }"
      class="builder-side-handle"
    />
  </template>
</template>

<style>
.vue-flow__node .builder-side-handle {
  width: 5px !important;
  height: 5px !important;
  background: transparent !important;
  border: 1px solid transparent !important;
  border-radius: 1px !important;
  opacity: 0 !important;
  transition: opacity 0.15s, background 0.15s, border-color 0.15s;
}
.vue-flow__node:hover .builder-side-handle {
  opacity: 0.9 !important;
  background: rgba(154, 171, 209, 0.35) !important;
  border-color: rgba(154, 171, 209, 0.6) !important;
}
</style>
