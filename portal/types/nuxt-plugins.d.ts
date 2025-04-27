import type { User } from "@prisma/client"; // Import User type if needed

// Define the structure of the object provided by the auth plugin
interface NuxtAuth {
  getState: () => {
    isAuthenticated: boolean;
    user: User | null;
    token: string | null;
  };
  getUser: () => User | null;
  getToken: () => string | null;
  login: (token: string, userData?: User | null) => void;
  logout: () => void;
  updateProfile: (updateData: Record<string, unknown>) => Promise<User>; // Assuming it returns the updated user
  check: () => boolean;
  hasRole: (role: string) => boolean;
  isAdmin: () => boolean;
  isSecurityOrAdmin: () => boolean;
}

// Augment the NuxtApp interface
declare module "#app" {
  interface NuxtApp {
    $auth: NuxtAuth; // Declare that $auth exists and has the NuxtAuth type
  }
}

// Augment the Vue instance properties if using Options API (less common with Nuxt 3 setup)
// declare module 'vue' {
//   interface ComponentCustomProperties {
//     $auth: NuxtAuth;
//   }
// }

export {};
