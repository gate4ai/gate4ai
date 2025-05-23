import type { User } from "@prisma/client";
import type { FetchOptions } from "ofetch";

// Define a basic structure for the auth plugin interface
interface NuxtAuth {
  isAdmin: () => boolean;
  isSecurityOrAdmin: () => boolean;
  // Add other methods used by the settings page if necessary
  getUser: () => User | null;
  getToken: () => string | null;
  check: () => boolean;
}

// Define a basic structure for the api plugin interface
interface NuxtApi {
  getJson: <T = any>(url: string, options?: FetchOptions) => Promise<T>;
  postJson: <T = any>( // Added postJson
    url: string,
    data?: Record<string, unknown> | BodyInit | null,
    options?: FetchOptions
  ) => Promise<T>;
  putJson: <T = any>(
    url: string,
    data?: Record<string, unknown> | BodyInit | null,
    options?: FetchOptions
  ) => Promise<T>;
  // Add other methods used by the settings page if necessary
}

// Augment the NuxtApp interface
declare module "#app" {
  interface NuxtApp {
    $auth: NuxtAuth;
    $api: NuxtApi;
    // Define $settings if needed by other parts, though settings.vue accesses it differently
    $settings: {
      get: (key: string) => unknown;
      // Add other methods if needed
    };
  }
}

// You might need to declare this empty export if the file only contains type declarations
export {};
