export default defineNuxtRouteMiddleware((to, _from) => {
  console.log('Auth middleware executing for route:', to.fullPath);
  
  // Only access localStorage in client-side
  if (import.meta.client) {
    const token = localStorage.getItem('auth_token');
    console.log('Auth token exists:', !!token);
    
    // If user is not authenticated and trying to access a protected route
    if (!token) {
      console.log('No auth token found, redirecting to login');
      return navigateTo(`/login?redirect=${to.fullPath}`);
    }
    console.log('User is authenticated, allowing access to:', to.fullPath);
  } else {
    // For server-side rendering, don't redirect - let the page render
    // Client-side hydration will check auth after page loads
    console.log('Server-side rendering for:', to.fullPath, '- continue without redirect');
  }
}) 