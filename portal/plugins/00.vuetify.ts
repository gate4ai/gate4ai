// plugins/vuetify.ts
import { createVuetify } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'

export default defineNuxtPlugin(nuxtApp => {
  const vuetify = createVuetify({
    components,
    directives,
    theme: {
      defaultTheme: 'light',
      themes: {
        light: {
          dark: false,
          colors: {
            primary: '#1867C0',
            secondary: '#5CBBF6',
            accent: '#4CAF50',
            error: '#FF5252',
            info: '#2196F3',
            success: '#4CAF50',
            warning: '#FFC107'
          }
        },
        dark: {
          dark: true,
          colors: {
            primary: '#2196F3',
            secondary: '#424242',
            accent: '#FF4081',
            error: '#FF5252',
            info: '#2196F3',
            success: '#4CAF50',
            warning: '#FB8C00'
          }
        }
      }
    }
  })

  nuxtApp.vueApp.use(vuetify)
}) 