/**
 * Centralized validation rules for forms across the application
 */

/**
 * Common validation rules for form fields
 */
export const rules = {
  /**
   * Required field validation
   * @param v - Field value
   * @returns True if valid or error message
   */
  required: (v: unknown): boolean | string => !!v || "This field is required",

  /**
   * Email validation
   * @param v - Email value
   * @returns True if valid or error message
   */
  email: (v: string): boolean | string =>
    !v || /.+@.+\..+/.test(v) || "Please enter a valid email",

  /**
   * Password validation (minimum 8 characters)
   * @param v - Password value
   * @returns True if valid or error message
   */
  password: (v: string): boolean | string =>
    (v && v.length >= 8) || "Password must be at least 8 characters",

  /**
   * URL validation
   * @param v - URL value
   * @returns True if valid, true if empty, or error message
   */
  url: (v: string): boolean | string =>
    !v || /^https?:\/\/[^\s$.?#].[^\s]*$/.test(v) || "Please enter a valid URL",

  /**
   * Simple URL validation that just checks if URL starts with http:// or https://
   * @param v - URL value
   * @returns True if valid, true if empty, or error message
   */
  simpleUrl: (v: string): boolean | string =>
    !v || /^https?:\/\//.test(v) || "URL must start with http:// or https://",

  /**
   * Server URL validation (required and must be a URL)
   * @param v - URL value
   * @returns Array of validation rules
   */
  serverUrl: [
    (v: string): boolean | string => !!v || "Server URL is required",
    (v: string): boolean | string =>
      /^https?:\/\//.test(v) || "URL must start with http:// or https://",
  ],

  /**
   * Dynamic password confirmation validator
   * @param password - Reference to the password to match
   * @returns Validation function
   */
  confirmPassword:
    (password: string) =>
    (v: string): boolean | string =>
      v === password || "Passwords do not match",

  /**
   * Checkbox agreement validation
   * @param v - Checkbox value
   * @returns True if checked or error message
   */
  agree: (v: boolean): boolean | string =>
    v || "You must agree to the terms to continue",

  /**
   * JSON format validation
   * @param v - JSON string
   * @returns True if valid or error message
   */
  json: (v: string): boolean | string => {
    if (!v) return true; // Allow empty input if not required elsewhere
    try {
      JSON.parse(v);
      return true;
    } catch {
      return "Invalid JSON format";
    }
  },

  /**
   * Slug format validation (lowercase letters, numbers, hyphens)
   * @param v - Slug value
   * @returns True if valid or error message
   */
  slugFormat: (v: string): boolean | string =>
    /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(v) ||
    "Slug must contain only lowercase letters, numbers, and hyphens, and cannot start or end with a hyphen.",

  // Note: Slug uniqueness rule is handled asynchronously in the component (`AddServerDialog.vue`)
  // due to needing API calls. It's implemented as `slugUniqueRule` there.
};
