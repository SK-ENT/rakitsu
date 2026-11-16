import { reactive } from 'vue';

// Panels share event IDs only when they have the same recorded session open.
export const recordedReplayCursor = reactive(new Map<string, string>());
