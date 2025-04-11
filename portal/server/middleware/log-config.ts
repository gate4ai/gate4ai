export default defineEventHandler((event) => {
    const config = useRuntimeConfig(event) 
    console.log('[Middleware log-config] JWT Secret:', config.jwtSecret);
    console.log('[Middleware log-config] Type of jwtSecret:', typeof config.jwtSecret);
    console.log('[Middleware log-config] process.env.JWT_SECRET:', process.env.JWT_SECRET);

    console.log('[Middleware log-config] TestTest:', config.testTest);
    console.log('[Middleware log-config] Type of TestTest:', typeof config.testTest);
    console.log('[Middleware log-config] process.env.TEST_TEST:', process.env.TEST_TEST);

    console.log('[Middleware log-config] process.env:', process.env);
    console.log('[Middleware log-config] config:', config);
  })