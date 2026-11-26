// The pure half of the ability picker (ResourceBindingSelect.vue).
//
// Binding an agent to mcp servers and skill manuals is an edit of a resource-id
// array, and the one rule that matters is negative: a reference the operator
// cannot currently see must survive the edit. Keeping that rule here makes it
// testable without a DOM, which this console deliberately does not ship.

/**
 * References that are selected but not offered by the catalogue. They are a
 * stale id, a record outside the caller's grants, or a free-text skill label
 * written before bindings existed — the picker keeps all three.
 */
export function unresolvedRefs(selected, options) {
  const known = new Set((options ?? []).map((entry) => entry.id))
  return (selected ?? []).filter((ref) => ref && !known.has(ref))
}

/** Case-insensitive search over the fields an operator recognises a record by. */
export function filterOptions(options, query) {
  const list = options ?? []
  const needle = String(query ?? '').trim().toLowerCase()
  if (!needle) return list
  return list.filter((entry) =>
    [entry.name, entry.display_name, entry.namespace, entry.id]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle)),
  )
}

/** Add or remove one reference, preserving the order the operator selected. */
export function toggleRef(selected, id) {
  const current = (selected ?? []).filter(Boolean)
  return current.includes(id) ? current.filter((ref) => ref !== id) : [...current, id]
}
