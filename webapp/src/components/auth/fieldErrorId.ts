/** Id of the element describing a field, so its control can point at it. */
export function fieldErrorId(id: string): string {
  return `${id}-error`;
}

/** Id of a field's standing hint — the constraint line under the control. */
export function fieldHintId(id: string): string {
  return `${id}-hint`;
}

/**
 * The `aria-describedby` a control needs: its hint, its error, or both. A
 * control with neither gets `undefined` rather than an empty attribute.
 */
export function fieldDescribedBy(
  id: string,
  parts: { hint?: string | undefined; error?: string | undefined },
): string | undefined {
  const ids = [
    ...(parts.hint === undefined ? [] : [fieldHintId(id)]),
    ...(parts.error === undefined ? [] : [fieldErrorId(id)]),
  ];
  return ids.length === 0 ? undefined : ids.join(" ");
}
