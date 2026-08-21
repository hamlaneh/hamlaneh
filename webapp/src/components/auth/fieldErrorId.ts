/** Id of the element describing a field, so its control can point at it. */
export function fieldErrorId(id: string): string {
  return `${id}-error`;
}
