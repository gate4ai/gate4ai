import type { User } from "@prisma/client";

// This plugin enables client-side authentication handling
export default defineNuxtPlugin(() => {
  // Add a global auth state that can be used throughout the app
  const authState = useState("auth", () => ({
    isAuthenticated: false,
    user: null as User | null,
    token: null as string | null,
  }));

  // Initialize auth state on client side
  if (import.meta.client) {
    const token = localStorage.getItem("auth_token");
    const userJson = localStorage.getItem("auth_user");
    if (token) {
      authState.value.isAuthenticated = true;
      authState.value.token = token;

      // Also load the user data if available
      if (userJson) {
        try {
          authState.value.user = JSON.parse(userJson);
        } catch (e) {
          console.error("Failed to parse user data from localStorage", e);
        }
      }
    }
  }

  // Provide helper functions
  return {
    provide: {
      auth: {
        // Get current auth state
        getState: () => authState.value,

        // Get current user
        getUser: () => authState.value.user,

        // Get auth token
        getToken: () => authState.value.token,

        // Login handler
        login: (token: string, userData: User | null = null) => {
          if (import.meta.client) {
            localStorage.setItem("auth_token", token);
            if (userData) {
              localStorage.setItem("auth_user", JSON.stringify(userData));
            }
          }
          authState.value.isAuthenticated = true;
          authState.value.token = token;
          authState.value.user = userData;
        },

        // Logout handler
        logout: () => {
          if (import.meta.client) {
            localStorage.removeItem("auth_token");
            localStorage.removeItem("auth_user");
          }
          authState.value.isAuthenticated = false;
          authState.value.token = null;
          authState.value.user = null;
          navigateTo("/");
        },

        // Update user profile
        updateProfile: async (updateData: Record<string, unknown>) => {
          if (!authState.value.user || !authState.value.token) {
            throw new Error("User not authenticated");
          }

          const userId = authState.value.user.id;
          const { $api } = useNuxtApp();

          // Call the API to update the user profile
          const updatedUser = await $api.putJson(
            `/users/${userId}`,
            updateData
          );

          // Update the local user state with the updated data
          authState.value.user = updatedUser;

          // Update localStorage if we're on the client
          if (import.meta.client) {
            localStorage.setItem("auth_user", JSON.stringify(updatedUser));
          }

          return updatedUser;
        },

        // Check if user is authenticated
        check: () => {
          return authState.value.isAuthenticated;
        },

        // Check if user has a specific role
        hasRole: (role: string) => {
          return authState.value.user && authState.value.user.role === role;
        },

        // Check if user is admin
        isAdmin: () => {
          return authState.value.user && authState.value.user.role === "ADMIN";
        },
        // Check if user is admin or security
        isSecurityOrAdmin: () => {
          return (
            authState.value.user &&
            (authState.value.user.role === "ADMIN" ||
              authState.value.user.role === "SECURITY")
          );
        },
      },
    },
  };
});
