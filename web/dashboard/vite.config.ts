import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from "path"

// In development (npm run dev), API calls go to a running Smart Proxy: SMART_PROXY_URL
// (default http://localhost:8081), with SMART_PROXY_AUTH ("user:password") for basic auth.
const target = process.env.SMART_PROXY_URL || "http://localhost:8081"
const auth = process.env.SMART_PROXY_AUTH

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    proxy: Object.fromEntries(["/api", "/auth"].map(prefix => [prefix, {
      target,
      changeOrigin: true,
      headers: auth ? { Authorization: "Basic " + Buffer.from(auth).toString("base64"), Origin: target } : { Origin: target },
    }])),
  },
})
