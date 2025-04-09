// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2024-11-01',
  devtools: { enabled: true },
  
  modules: [
    '@nuxtjs/google-fonts',
    // Vuetify will be configured in plugins
  ],
  
  // Google Fonts configuration
  googleFonts: {
    families: {
      Roboto: true,
      'Open+Sans': [400, 500, 600, 700],
    },
    download: true,
    inject: true,
  },
  
  css: [
    'vuetify/lib/styles/main.sass',
    '@mdi/font/css/materialdesignicons.min.css',
  ],
  
  build: {
    transpile: ['vuetify'],
  },
  
  runtimeConfig: {
    jwtSecret: process.env.JWT_SECRET,
    public: {
      apiBaseUrl: process.env.API_BASE_URL || '/api',
    }
  },
  
  app: {
    head: {
      title: 'gate4.ai - AI tools security governance',
      meta: [
        { name: 'description', content: 'Enterprise MCP Server Management Platform' }
      ],
    }
  }
})
