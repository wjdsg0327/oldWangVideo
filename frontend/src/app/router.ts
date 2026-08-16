import { createRouter, createWebHashHistory } from "vue-router";
import GenerationWorkbench from "../views/GenerationWorkbench.vue";
import HistoryView from "../views/HistoryView.vue";
import ProvidersView from "../views/ProvidersView.vue";
import TemplatesView from "../views/TemplatesView.vue";

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: "/", redirect: "/generate/text_to_image" },
    { path: "/generate/:capability", component: GenerationWorkbench },
    { path: "/providers", component: ProvidersView },
    { path: "/templates", component: TemplatesView },
    { path: "/history", component: HistoryView, props: { favoriteOnly: false } },
    { path: "/favorites", component: HistoryView, props: { favoriteOnly: true } },
  ],
});
