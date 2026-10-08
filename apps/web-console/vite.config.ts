import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the API is proxied so the browser talks to one origin.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": process.env.VITE_API_PROXY ?? "http://localhost:8080" },
  },
  test: { environment: "jsdom" },
} as never);
