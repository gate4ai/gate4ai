// /home/alex/go-ai/gate4ai/www/composables/useSnackbar.ts
import { readonly } from 'vue'; // ref is no longer needed as we're using let for timer
import { useState } from '#app'; // Correct import

// Define the shape of the snackbar state
interface SnackbarState {
  show: boolean;
  text: string;
  color: string;
  timeout: number;
}

// No useState call here in the module scope

// The composable function itself
export function useSnackbar() {
  // Call useState *inside* the composable function.
  // This ensures it's called within a valid Nuxt/Vue context (like setup).
  // The factory function () => ({...}) runs only once globally for this key.
  const snackbarState = useState<SnackbarState>('snackbar', () => ({
    show: false,
    text: '',
    color: 'info', // Default color
    timeout: 3000, // Default timeout
  }));

  // Keep timer management logic inside the composable function scope
  // Using ref allows the timer ID to persist across potential re-renders if needed,
  // though a simple let might suffice if the composable instance lifetime matches component.
  // Using let here as the state itself is the primary reactive element.
  let timer: NodeJS.Timeout | null = null;

  const showSnackbar = (text: string, color: string = 'info', timeout: number = 3000) => {
    // Clear existing timer if a new snackbar is shown quickly
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }

    // Modify the shared state returned by useState
    snackbarState.value = {
      text,
      color,
      timeout, // Store timeout in state if needed by components, otherwise maybe not
      show: true,
    };

    // Set a timer to hide the snackbar automatically
    if (timeout > 0) {
      timer = setTimeout(() => {
        hideSnackbar();
      }, timeout);
    }
  };

  const hideSnackbar = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    // Ensure state exists before trying to modify
    if (snackbarState.value) {
        snackbarState.value.show = false;
    }
  };

  // Return the reactive state (read-only) and methods
  return {
    snackbar: readonly(snackbarState), // Use readonly for external safety
    showSuccess: (text: string, timeout: number = 3000) => showSnackbar(text, 'success', timeout),
    showError: (text: string, timeout: number = 5000) => showSnackbar(text, 'error', timeout), // Longer timeout for errors
    showInfo: (text: string, timeout: number = 3000) => showSnackbar(text, 'info', timeout),
    showWarning: (text: string, timeout: number = 4000) => showSnackbar(text, 'warning', timeout),
    hideSnackbar, // Expose hide method if needed externally
  };
}