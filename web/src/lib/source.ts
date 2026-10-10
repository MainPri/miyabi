export type SourceScope =
  | { account_id: string; directory_id: string }
  | { account_id: string; directory: { id: string } }

export function sameSource(a?: SourceScope | null, b?: SourceScope | null): boolean {
  if (!a || !b) return false
  const dirA = 'directory_id' in a ? a.directory_id : a.directory.id
  const dirB = 'directory_id' in b ? b.directory_id : b.directory.id
  return a.account_id === b.account_id && dirA === dirB
}

export function sourceKey(source?: SourceScope | null): string {
  if (!source) return ''
  const dir = 'directory_id' in source ? source.directory_id : source.directory.id
  return `${source.account_id}:${dir}`
}

export function scopeDirectoryID(scope?: SourceScope | null): string {
  if (!scope) return ''
  return 'directory_id' in scope ? scope.directory_id : scope.directory.id
}

// inDirectories reports whether a source scope points at one of the mounted directories.
export function inDirectories(scope?: SourceScope | null, directories?: Array<{ id: string }> | null): boolean {
  if (!scope || !directories?.length) return false
  const dir = scopeDirectoryID(scope)
  return directories.some(directory => directory.id === dir)
}

// mountedScopeKey builds a stable identity for a set of mounted directories.
export function mountedScopeKey(source?: SourceScope | null, directories?: Array<{ id: string }> | null): string {
  if (!directories?.length) return sourceKey(source)
  const ids = directories.map(directory => directory.id).sort().join(',')
  return `${source?.account_id ?? ''}:[${ids}]`
}
