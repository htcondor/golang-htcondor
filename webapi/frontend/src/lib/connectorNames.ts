/**
 * What to call a connector in front of a person.
 *
 * A connector's id is `<namespace>:<name>` -- `builtin:gdrive`, `site:lab
 * drive` -- which is how the server identifies it and not how anybody says
 * it. Two surfaces need the friendly form: the file chooser's buttons and the
 * Connectors page's list of connected accounts. One map, because the second
 * copy is the one that falls behind when a provider is added.
 */

/** Provider names, keyed by the connector or picker provider name. */
export const providerNames: Record<string, string> = {
  gdrive: 'Google Drive',
};

/**
 * connectorLabel renders a connector id for display.
 *
 * The namespace is dropped: `builtin:` says where the definition came from,
 * which is an operator's concern and not the name of the service. A site
 * connector keeps its own name, since that is what the operator called it and
 * there is nothing friendlier to substitute.
 */
export function connectorLabel(id: string): string {
  const name = id.includes(':') ? id.slice(id.indexOf(':') + 1) : id;
  return providerNames[name] ?? name;
}
