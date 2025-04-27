const baseModules = [
  "@nuxtjs/google-fonts",
  // Vuetify will be configured in plugins
];
const analyticsModules =
  process.env.DISABLE_ANALYTICS === "true"
    ? []
    : ["nuxt-gtag", "yandex-metrika-module-nuxt3"];
const enabledModules = [...baseModules, ...analyticsModules];

// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: "2024-11-01",
  devtools: { enabled: true },

  modules: enabledModules,

  gtag: {
    id: "G-SVYXLTWN67",
  },

  yandexMetrika: {
    id: "101287823",
    clickmap: true,
    trackLinks: true,
    accurateTrackBounce: true,
    webvisor: true,
  },

  // Google Fonts configuration
  googleFonts: {
    families: {
      Roboto: true,
      "Open+Sans": [400, 500, 600, 700],
    },
    download: true,
    inject: true,
  },

  css: [
    "vuetify/lib/styles/main.sass",
    "@mdi/font/css/materialdesignicons.min.css",
  ],

  build: {
    transpile: ["vuetify"],
  },

  runtimeConfig: {
    jwtSecret: process.env.NUXT_JWT_SECRET,
    public: {
      apiBaseUrl: process.env.NUXT_API_BASE_URL || "/api",
      gate4aiNotification: process.env.NUXT_GATE4AI_NOTIFICATION || "",
    },
  },

  app: {
    head: {
      title: "gate4.ai - AI tools security governance",
      meta: [
        {
          name: "description",
          content: "Enterprise MCP Server Management Platform",
        },
      ],
    },
  },
});
