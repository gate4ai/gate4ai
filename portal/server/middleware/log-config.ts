export default defineEventHandler((event) => {
    const config = useRuntimeConfig(event) 
    console.log('[Middleware log-config] JWT Secret on request:', config.portalJwtSecret);
    console.log('[Middleware log-config] Type of jwtSecret:', typeof config.portalJwtSecret);
    console.log('[Middleware log-config] process.env.JWT_SECRET:', process.env.PORTAL_JWT_SECRET);
  })