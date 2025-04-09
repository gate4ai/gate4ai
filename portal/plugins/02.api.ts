//////////////////////////
// /home/alex/go-ai/gate4ai/www/plugins/02.api.ts
//////////////////////////
// /home/alex/go-ai/gate4ai/www/plugins/02.api.ts
import { type FetchError, type FetchOptions, $fetch } from 'ofetch';
import type { H3Error } from 'h3';
import { useRuntimeConfig, useRequestURL, useNuxtApp } from '#app';

// --- BEGIN: Helper functions and interfaces (Keep these as they are) ---

// Interface for a single validation error detail
interface ValidationError {
  field: string;
  message: string;
}

// Interface for the expected structure of error data
interface ErrorDataForFormatting {
  message?: string;
  fieldErrors?: Record<string, string[] | undefined>;
  errors?: ValidationError[] | string[];
  // Allow for direct string or object data which might contain a message
  data?: { message?: string } | string;
}

/**
 * Formats validation errors into a single string.
 * @param errorData - The 'data' part of the FetchError.
 * @returns A formatted string or null.
 */
function formatValidationErrors(errorData: ErrorDataForFormatting): string | null {
  const messages: string[] = [];

  if (errorData.fieldErrors) {
    Object.entries(errorData.fieldErrors).forEach(([field, fieldMessages]) => {
      if (fieldMessages && fieldMessages.length > 0) {
        messages.push(`${field}: ${fieldMessages.join(', ')}`);
      }
    });
  } else if (Array.isArray(errorData.errors)) {
    errorData.errors.forEach(err => {
      if (typeof err === 'string') {
        messages.push(err);
      } else if (typeof err === 'object' && err.field && err.message) {
        messages.push(`${err.field}: ${err.message}`);
      }
    });
  }

  if (messages.length > 0) {
    // Combine with the main message if it exists
    const mainMessage = typeof errorData.message === 'string' ? `${errorData.message}: ` : 'Validation failed: ';
    return `${mainMessage}${messages.join('; ')}`;
  }
  return null;
}

/**
 * Handles API errors from ofetch, extracting a user-friendly message.
 * @param error - The error object.
 * @returns A user-friendly error message string.
 */
async function handleApiError(error: unknown): Promise<string> {
    if (error instanceof Error && 'response' in error && error.response) {
        const fetchError = error as FetchError;
        const response = fetchError.response;
        if (!response) {
          return 'Network error or invalid response received.'; // More specific message
        }
        // Attempt to parse error data, be robust against different structures
        const errorData = response._data as ErrorDataForFormatting | null | undefined;

        let detailedMessage: string | null = null;

        if (errorData) {
            detailedMessage = formatValidationErrors(errorData); // Check validation first
            if (!detailedMessage && typeof errorData.message === 'string') {
                 detailedMessage = errorData.message; // Use top-level message
            } else if (!detailedMessage && typeof errorData.data === 'object' && errorData.data?.message) {
                 detailedMessage = errorData.data.message; // Use nested message if available
            } else if (!detailedMessage && typeof errorData === 'string') {
                 detailedMessage = errorData; // Handle case where _data is just a string message
            }
        }

        // Use detailed message if found, otherwise fallback based on status
        if (detailedMessage) return detailedMessage;

        // Fallback messages based on status code
        if (response.statusText) {
            if (response.status === 401) return 'Unauthorized. Please check your login credentials or API key.';
            if (response.status === 403) return 'Forbidden. You do not have permission to perform this action.';
            if (response.status === 404) return `Resource not found (${response.url}).`;
            if (response.status >= 400 && response.status < 500) return `Client Error ${response.status}: ${response.statusText}`;
            if (response.status >= 500) return `Server Error ${response.status}: ${response.statusText}. Please try again later.`;
            return `Error ${response.status}: ${response.statusText}`;
        }
        return `HTTP Error: ${response.status}`;
    }

    // Handle H3 specific errors (might occur in SSR context)
    if (error instanceof Error && 'statusCode' in error && 'statusMessage' in error) {
        const h3Error = error as H3Error;
        return h3Error.statusMessage || `Error ${h3Error.statusCode}`;
    }

    // Handle generic JavaScript errors
    if (error instanceof Error) {
        return error.message || 'An unexpected error occurred.'; // Ensure a message is returned
    }

    // Fallback for non-Error types
    return 'An unknown error occurred. Please check the console for details.';
}


// --- END: Helper functions and interfaces ---


export default defineNuxtPlugin(() => {
  // console.log('[Plugin 02.api.ts] Starting setup...'); // Reduced logging noise
  const { $auth } = useNuxtApp(); // $auth should be available
  const config = useRuntimeConfig();
  const publicApiBase = config.public.apiBaseUrl || '/api';

  let resolvedBaseURL: string;

  // Determine baseURL for the standard API fetcher (prefixed)
  if (import.meta.server) {
    // console.log('[Plugin 02.api.ts] [SSR] Determining baseURL for apiFetcher...');
    if (publicApiBase.startsWith('http://') || publicApiBase.startsWith('https://')) {
       resolvedBaseURL = publicApiBase;
       // console.log(`[Plugin 02.api.ts] [SSR] Using absolute publicApiBase: ${resolvedBaseURL}`);
    } else {
       const _reqUrl = useRequestURL(); // Use RequestURL to get current host/port on SSR
       // Ensure relative path starts with '/'
       const relativePath = publicApiBase.startsWith('/') ? publicApiBase : `/${publicApiBase}`;
       // Construct URL relative to the current request's origin
       resolvedBaseURL = new URL(relativePath, _reqUrl.origin).toString();
       // console.log(`[Plugin 02.api.ts] [SSR] Constructed internal baseURL: ${resolvedBaseURL}`);
    }
  } else {
    resolvedBaseURL = publicApiBase;
    // console.log(`[Plugin 02.api.ts] [Client] Using publicApiBase: ${resolvedBaseURL}`);
  }

  // --- Shared Configuration Logic ---

  // Shared onRequest handler to add Authorization token
  const commonOnRequest = async ({ options }: { options: FetchOptions }) => {
    const token = $auth.getToken();
    if (token) {
      options.headers = new Headers(options.headers); // Ensure headers is a Headers object
      if (!options.headers.has('Authorization')) { // Avoid overwriting if already set
           options.headers.set('Authorization', `Bearer ${token}`);
      }
    }
     // Ensure Content-Type for JSON posts/puts if not already set
     if ((options.method === 'POST' || options.method === 'PUT') && options.body && typeof options.body === 'object') {
        options.headers = new Headers(options.headers);
        if (!options.headers.has('Content-Type')) {
           options.headers.set('Content-Type', 'application/json');
        }
        if (!options.headers.has('Accept')) {
            options.headers.set('Accept', 'application/json'); // Expect JSON back
        }
     }
  };

  // Shared onResponseError handler for logging
  const commonOnResponseError = async ({ request, response, error, options }: { request: RequestInfo | URL; response?: Response; error?: Error; options: FetchOptions }) => {
      // Avoid logging simple 401/403/404 errors as severe errors unless debugging
      const statusCode = response?.status;
      const logLevel = (statusCode && statusCode >= 400 && statusCode < 500 && statusCode !== 422) ? 'warn' : 'error'; // Log 4xx as warn, 5xx as error, 422 (validation) as error

      // Use handleApiError to get a consistent message for logging
      const errorMessageForLog = error ? await handleApiError(error) : `Status ${statusCode}`;
      console[logLevel](`[Plugin 02.api.ts] API Request Failed: ${options.method} ${request.toString()} -> Status=${statusCode || 'N/A'} | Message: ${errorMessageForLog}`, error ? error : ''); // Log original error too
  };

  // --- Create Fetcher Instances ---

  // 1. Standard API Fetcher (with baseURL like /api)
  const apiFetcher = $fetch.create({
    baseURL: resolvedBaseURL,
    onRequest: commonOnRequest, // Use shared handler
    onResponseError: commonOnResponseError, // Use shared handler
    retry: 0, // Disable automatic retries by default, handle manually if needed
  });

  // 2. Root API Fetcher (without baseURL) - Useful for external or non-prefixed calls
  const rootFetcher = $fetch.create({
    // No baseURL defined here! Requests go relative to the current page or absolute if full URL provided.
    onRequest: commonOnRequest, // Reuse shared handler for auth token
    onResponseError: commonOnResponseError, // Reuse shared handler for logging
    retry: 0,
  });

  // --- Define API Methods ---

  // Helper to ensure URL path starts with '/' if needed (less critical with baseURL set)
  // const ensureLeadingSlash = (url: string): string => url.startsWith('/') ? url : `/${url}`;

  // Define the methods that will be available on $api
  const providedApi = {
    // --- Methods using standard apiFetcher (baseURL applies) ---

    /** Makes a GET request to the configured API base URL. */
    async getJson<T>(url: string, options: FetchOptions = {}): Promise<T> {
      try {
        // const effectiveUrl = ensureLeadingSlash(url); // Less needed with baseURL
        return await apiFetcher<T>(url, { ...options, method: 'GET' });
      } catch (error: unknown) {
        const errorMessage = await handleApiError(error);
        // console.error(`[Plugin 02.api.ts] Error in getJson(${url}): ${errorMessage}`, error instanceof Error ? error : error); // Log full error object
        throw new Error(errorMessage); // Throw a new error with the formatted message
      }
    },
    /** Makes a POST request with JSON body to the configured API base URL. */
    async postJson<T>(url: string, data?: Record<string, unknown> | BodyInit | null, options: FetchOptions = {}): Promise<T> {
       try {
         // const effectiveUrl = ensureLeadingSlash(url);
         return await apiFetcher<T>(url, { method: 'POST', body: data, ...options });
       } catch (error: unknown) {
         const errorMessage = await handleApiError(error);
         // console.error(`[Plugin 02.api.ts] Error in postJson(${url}): ${errorMessage}`, error instanceof Error ? error : error);
         throw new Error(errorMessage);
       }
    },
     /** Makes a PUT request with JSON body to the configured API base URL. */
    async putJson<T>(url: string, data?: Record<string, unknown> | BodyInit | null, options: FetchOptions = {}): Promise<T> {
      try {
        // const effectiveUrl = ensureLeadingSlash(url);
        return await apiFetcher<T>(url, { method: 'PUT', body: data, ...options });
      } catch (error: unknown) {
        const errorMessage = await handleApiError(error);
        // console.error(`[Plugin 02.api.ts] Error in putJson(${url}): ${errorMessage}`, error instanceof Error ? error : error);
        throw new Error(errorMessage);
      }
    },
    /** Makes a DELETE request to the configured API base URL. Handles 204 No Content. */
    async deleteJson<T = void>(url: string, options: FetchOptions = {}): Promise<T> { // Default T to void for 204 cases
      try {
        // const effectiveUrl = ensureLeadingSlash(url);
        // Use .raw() to check status code before parsing JSON
        const response = await apiFetcher.raw(url, { method: 'DELETE', ...options });

        if (response.status === 204) {
             // Return an empty object or undefined cast to T for 204 No Content
             // Explicitly returning undefined might be cleaner if T can be void
             return undefined as T;
        }
        // For other success statuses (e.g., 200 OK with body, 202 Accepted),
        // return the parsed body (_data is populated by ofetch).
        // If T is expected, cast it. If T is void, this might be {} or null.
        return response._data as T;
      } catch (error: unknown) {
         // Let handleApiError format the message, including 404
         const errorMessage = await handleApiError(error);
         // console.error(`[Plugin 02.api.ts] Error in deleteJson(${url}): ${errorMessage}`, error instanceof Error ? error : error);
         throw new Error(errorMessage);
      }
    },

    /** Makes a GET request to a specific absolute URL (ignores configured baseURL). */
    async getJsonByRawURL<T>(url: string, options: FetchOptions = {}): Promise<T> {
      try {
        // Use the rootFetcher which doesn't have a baseURL
        return await rootFetcher<T>(url, { ...options, method: 'GET' });
      } catch (error: unknown) {
        const errorMessage = await handleApiError(error);
        // console.error(`[Plugin 02.api.ts] Error in getJsonByRawURL(${url}): ${errorMessage}`, error instanceof Error ? error : error);
        throw new Error(errorMessage);
      }
    },

     /** Makes a POST request with JSON body to a specific absolute URL (ignores configured baseURL). */
    async postJsonByRawURL<T>(url: string, data?: Record<string, unknown> | BodyInit | null, options: FetchOptions = {}): Promise<T> {
      try {
         // Use the rootFetcher which doesn't have a baseURL
         return await rootFetcher<T>(url, { method: 'POST', body: data, ...options });
      } catch (error: unknown) {
        const errorMessage = await handleApiError(error);
        // console.error(`[Plugin 02.api.ts] Error in postJsonByRawURL(${url}): ${errorMessage}`, error instanceof Error ? error : error);
        throw new Error(errorMessage);
      }
    },
    // Add PUT/DELETE for raw URL if needed, following the same pattern using rootFetcher
  };

  // console.log('[Plugin 02.api.ts] Setup complete. Providing $api.'); // Reduced logging

  return {
    provide: {
      api: providedApi
    }
  };
});