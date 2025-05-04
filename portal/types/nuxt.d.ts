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
// Use 'unknown' instead of 'any' for better type safety
interface NuxtApi {
  getJson: <T = unknown>(url: string, options?: FetchOptions) => Promise<T>;
  postJson: <T = unknown>( // Added postJson
    url: string,
    data?: Record<string, unknown> | BodyInit | null,
    options?: FetchOptions
  ) => Promise<T>;
  putJson: <T = unknown>(
    url: string,
    data?: Record<string, unknown> | BodyInit | null,
    options?: FetchOptions
  ) => Promise<T>;
  // Add deleteJson method signature
  deleteJson: <T = unknown>(url: string, options?: FetchOptions) => Promise<T>;
  // Add postFormData method signature
  postFormData: <T = unknown>(
    url: string,
    formData: FormData,
    options?: FetchOptions
  ) => Promise<T>;
  // Add other methods used by the settings page if necessary
  getJsonByRawURL: <T = unknown>(
    url: string,
    options?: FetchOptions
  ) => Promise<T>;
  postJsonByRawURL: <T = unknown>(
    url: string,
    data?: Record<string, unknown> | BodyInit | null,
    options?: FetchOptions
  ) => Promise<T>;
}

// Augment the NuxtApp interface
declare module "#app" {
  interface NuxtApp {
    $auth: NuxtAuth;
    $api: NuxtApi;
    // Define $settings if needed by other parts, though settings.vue accesses it differently
    $settings: {
      get: (key: string) => unknown;
      getAll: () => Readonly<Record<string, unknown>>;
      isLoaded: () => boolean;
      isLoading: () => boolean;
      error: () => string | null;
      reload: () => Promise<void>;
    };
  }
}

// You might need to declare this empty export if the file only contains type declarations
export {};
