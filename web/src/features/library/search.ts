import { parseSearchPage } from '@/lib/search-schema'

export type ActorsSearch = {
  page?: number
  actor?: number
  name?: string
  actorPage?: number
}

export function validateActorsSearch(search: Record<string, unknown>): ActorsSearch {
  if (
    search.actor !== undefined &&
    typeof search.actor !== 'number' &&
    typeof search.actor !== 'string'
  ) {
    throw new Error('演员编号无效')
  }
  const actor = search.actor === undefined ? undefined : Number(search.actor)
  if (actor !== undefined && (!Number.isSafeInteger(actor) || actor < 1)) {
    throw new Error('演员编号无效')
  }
  const name =
    typeof search.name === 'string' && search.name.trim() ? search.name.trim() : undefined
  const page = parseSearchPage(search.page)
  const actorPage = parseSearchPage(search.actorPage)
  return {
    ...(page ? { page } : {}),
    ...(actor ? { actor, ...(name ? { name } : {}), ...(actorPage ? { actorPage } : {}) } : {})
  }
}
