// What the page and the service worker (sw.ts) agree on.

/** Paths under this prefix are served decrypted by the service worker. */
export const DECRYPTED_PREFIX = '/decrypted/'

/** Message type the worker sends to ask a page for a vault's content key. */
export const KEY_REQUEST = 'imagethat-key-request'
