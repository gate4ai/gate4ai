import { createHash } from 'crypto';

/**
 * Generates a secure random API key with the specified prefix
 * @param prefix Prefix for the key (e.g., "gk_")
 * @returns Generated API key
 */
export const generateApiKey = (prefix: string = 'g4_'): string => {
  const randomBytes = new Uint8Array(32);
  window.crypto.getRandomValues(randomBytes);
  
  const randomPart = Array.from(randomBytes)
    .map(b => b.toString(16).padStart(2, '0'))
    .join('');
  
  return `${prefix}${randomPart}`;
};

/**
 * Creates a SHA-256 hash of an API key
 * @param key The API key to hash
 * @returns SHA-256 hash of the key as a hex string
 */
export const hashApiKey = (key: string): string => {
  // Use SHA-256 for hashing
  const hash = createHash('sha256');
  hash.update(key);
  return hash.digest('hex');
};

/**
 * API key information object
 */
export interface ApiKeyInfo {
  key: string;       // Full API key (only available at creation time)
  keyHash: string;   // Hash of the API key (stored in database)
  displayHash: string; // Shortened hash for display
}

/**
 * Creates a new API key and returns both the key and its hash
 * @param prefix Prefix for the API key
 * @returns Object containing the key, its hash, and a display version of the hash
 */
export const createApiKey = (prefix: string = 'g4_'): ApiKeyInfo => {
  const key = generateApiKey(prefix);
  const keyHash = hashApiKey(key);
  
  return {
    key,
    keyHash,
    displayHash: keyHash.substring(0, 8) + '...' + keyHash.substring(keyHash.length - 8)
  };
}; 