import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { router } from "./app/router";
import { useAppStore } from "./stores/app";
import "./styles.css";

const pinia = createPinia();
const app = createApp(App).use(pinia).use(router);
const store = useAppStore(pinia);

// 先加载本地数据，再挂载页面，避免子视图在 bootstrap 完成前读取空状态。
void store.initialize().finally(() => app.mount("#app"));
