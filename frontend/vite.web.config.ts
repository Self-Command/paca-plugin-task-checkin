import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";
import { defineConfig } from "vite";
export default defineConfig({base:"/checkin/",plugins:[react(),tailwind()],build:{target:"es2020",outDir:"web-dist",rollupOptions:{input:"checkin.html"}}});
