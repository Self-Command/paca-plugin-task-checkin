import {scopePluginCss} from "./scope-css";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";
import federation from "@originjs/vite-plugin-federation";
import { defineConfig } from "vite";
export default defineConfig({plugins:[react(),scopePluginCss(),tailwind(),federation({name:"selfcommand_task_checkin",filename:"remoteEntry.js",exposes:{"./SettingsTab":"./src/SettingsTab.tsx","./CheckinSection":"./src/CheckinSection.tsx"},shared:{react:{requiredVersion:"^19.0.0"},"react-dom":{requiredVersion:"^19.0.0"},"@tanstack/react-query":{requiredVersion:"^5.0.0"}}})],build:{target:"esnext",minify:false,cssCodeSplit:false}});
