import { defineNuxtPlugin, useNuxtApp, useState } from "#app";

// Interface for the structure of a single setting from the API
interface Setting {
  id: string;
  key: string;
  group: string;
  name: string;
  description: string;
  value: unknown;
  frontend: boolean; // Indicates if the setting is needed on the frontend
  createdAt: string;
  updatedAt: string;
}

// Define the shape of the state managed by useState for reactivity and SSR/CSR consistency
interface SettingsState {
  settings: Record<string, unknown>;
  loaded: boolean;
  error: string | null;
  loading: boolean; // Track loading state to prevent concurrent loads
}

export default defineNuxtPlugin({
  name: "settings-loader",
  parallel: false, // Explicitly run after previous plugins (01, 02) have finished setup
  async setup(_nuxtApp) {
    // Renamed to _nuxtApp as we use useNuxtApp() inside loadSettings

    // Initialize reactive state using useState for proper hydration
    const state = useState<SettingsState>("app-settings", () => ({
      settings: {},
      loaded: false,
      error: null,
      loading: false,
    }));

    /**
     * Asynchronous function to fetch frontend settings from the API.
     * Updates the reactive state.
     */
    async function loadSettings() {
      // Prevent reloading if already loaded or currently loading
      if (state.value.loaded || state.value.loading) {
        // console.log('[Plugin 03.settings.ts] Settings load skipped (already loaded or loading).');
        return;
      }

      console.log("[Plugin 03.settings.ts] Entering loadSettings function...");
      state.value.loading = true; // Mark as loading
      state.value.error = null; // Reset error before attempting load

      try {
        // Get the Nuxt app instance inside the async function where it's needed
        const nuxt = useNuxtApp();

        // Explicitly check if $api and its method are available *at the time of execution*
        if (!nuxt.$api || typeof nuxt.$api.getJson !== "function") {
          const errorMsg =
            "$api or $api.getJson is not available when loadSettings was called. Check plugin execution order and lifecycle timing.";
          console.error("[Plugin 03.settings.ts] $api check failed:", errorMsg); // Shortened log
          throw new Error(errorMsg); // Throw error to be caught below
        }

        console.info(
          "[Plugin 03.settings.ts] Attempting to load frontend settings via $api.getJson..."
        );
        // Fetch only settings marked as `frontend: true` from the dedicated endpoint
        const data = await nuxt.$api.getJson<Setting[]>("/settings/frontend");
        console.info(
          `[Plugin 03.settings.ts] Frontend settings data received: ${
            data ? `(${data.length} items)` : "null/undefined"
          }`
        );

        const newSettings: Record<string, unknown> = {};
        if (data && Array.isArray(data)) {
          for (const setting of data) {
            newSettings[setting.key] = setting.value;
          }
        } else {
          console.warn(
            "[Plugin 03.settings.ts] Received non-array or empty data for frontend settings."
          );
        }

        // Update state reactively - this will trigger updates in components using $settings
        state.value.settings = newSettings;

        state.value.loaded = true; // Assume success if no exception thrown by $api.getJson
        console.info(
          "[Plugin 03.settings.ts] Frontend settings loaded and state updated successfully."
        );
      } catch (error: unknown) {
        const errorMsg =
          error instanceof Error
            ? error.message
            : "Failed to load settings due to an unknown error";
        console.error(
          "[Plugin 03.settings.ts] Error during loadSettings execution:",
          errorMsg,
          error
        );
        state.value.error = errorMsg; // Store the error message
        state.value.loaded = false; // Mark as not loaded on error
        state.value.settings = {}; // Clear potentially partial settings on error
      } finally {
        state.value.loading = false; // Mark as finished loading, regardless of success or failure
        console.log("[Plugin 03.settings.ts] Exiting loadSettings function.");
      }
    }

    // --- Loading Strategy ---

    // On the Server (SSR): Load settings when the app is created.
    // This makes settings available during server-side rendering if needed.
    if (import.meta.server) {
      _nuxtApp.hooks.hookOnce("app:created", async () => {
        console.log(
          "[Plugin 03.settings.ts] [SSR] app:created hook triggered."
        );
        if (!state.value.loaded) {
          // Check just in case (shouldn't be loaded yet on SSR)
          console.log(
            "[Plugin 03.settings.ts] [SSR] Attempting loadSettings from app:created."
          );
          await loadSettings(); // Await the loading process
        } else {
          console.log(
            "[Plugin 03.settings.ts] [SSR] Settings already marked as loaded in app:created, skipping load (unexpected)."
          );
        }
      });
    }

    // On the Client (CSR): Load settings *after* the app is mounted.
    // This ensures the DOM is ready and all initial client-side setup is likely complete.
    // It also loads settings if they weren't successfully loaded/hydrated from SSR.
    if (import.meta.client) {
      _nuxtApp.hooks.hookOnce("app:mounted", async () => {
        console.log(
          "[Plugin 03.settings.ts] [Client] app:mounted hook triggered."
        );
        // Only execute load if the state wasn't successfully loaded/hydrated from the server
        if (!state.value.loaded) {
          console.log(
            "[Plugin 03.settings.ts] [Client] Settings not loaded, executing loadSettings from app:mounted hook."
          );
          await loadSettings();
        } else {
          console.log(
            "[Plugin 03.settings.ts] [Client] Settings already loaded (likely hydrated from server), skipping load in app:mounted."
          );
        }
      });
    }

    console.log("[Plugin 03.settings.ts] Setup function completed."); // Log completion of the main setup function

    // Provide reactive access to the settings state
    return {
      provide: {
        settings: {
          // Access reactive state values directly
          get: (key: string): unknown => state.value.settings[key],
          getAll: (): Readonly<Record<string, unknown>> =>
            Object.freeze({ ...state.value.settings }), // Return immutable copy
          isLoaded: (): boolean => state.value.loaded,
          isLoading: (): boolean => state.value.loading, // Expose loading status
          error: (): string | null => state.value.error, // Expose error status
          // Function to manually trigger a reload of settings
          reload: async () => {
            console.info(
              "[Plugin 03.settings.ts] Reload requested. Forcing settings reload..."
            );
            state.value.loaded = false; // Reset loaded status to allow loadSettings to run
            await loadSettings(); // Call the loading function again
          },
        },
      },
    };
  },
});
