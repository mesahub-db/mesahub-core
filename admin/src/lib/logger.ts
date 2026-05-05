/**
 * Minimal structured logger for MesaHub Core.
 * All output goes to stdout/stderr so Railway captures it automatically.
 *
 * Format: [ISO timestamp] LEVEL  message
 * 
 * const VERSION = "1.0.1";
 */

function ts() {
  return new Date().toISOString();
}

export const logger = {
  info(msg: string) {
    console.log(`[${ts()}] INFO  ${msg}`);
  },
  warn(msg: string) {
    console.warn(`[${ts()}] WARN  ${msg}`);
  },
  error(msg: string) {
    console.error(`[${ts()}] ERROR ${msg}`);
  },
};
