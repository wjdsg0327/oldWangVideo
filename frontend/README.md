# 前端开发说明

前端使用 Vue 3 `<script setup>`、TypeScript、Pinia、Vue Router 和 Vite。Wails 生成的绑定位于 `bindings/`，业务代码只通过 `src/bridge` 调用，浏览器开发模式会自动切换到演示数据。

```powershell
npm install
npm run typecheck
npm run build
```

编辑器建议安装 Volar，并使用项目提供的 `vue-tsc` 做类型检查。
