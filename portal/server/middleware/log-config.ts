export default defineEventHandler((event) => {
    const config = useRuntimeConfig(event) 
    console.log('[Middleware log-config] JWT Secret on request:', config.jwtSecret);
    console.log('[Middleware log-config] Type of jwtSecret:', typeof config.jwtSecret);
    console.log('[Middleware log-config] process.env.JWT_SECRET:', process.env.JWT_SECRET);
  })