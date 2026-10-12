import assert from 'node:assert/strict'
import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { test, onTestFinished } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import {
  libraryKeys,
  type LibraryActorsPage as LibraryActorsPageData,
  type LibraryActor,
  type LibraryPage
} from '@/api/library'
import { LibraryActorCard } from '@/features/library/actor-card'
import { LibraryActorsPage } from '@/features/library/actors-page'
import { validateActorsSearch } from '@/features/library/search'
import { MovieDetailDialogContext } from '@/features/movie-detail/dialog-context'
import { TooltipProvider } from '@/components/ui/tooltip'

function actor(overrides: Partial<LibraryActor> = {}): LibraryActor {
  return {
    id: 5,
    provider: 'javdb',
    source_id: 'actor-5',
    name: 'X actor',
    movie_count: 3,
    ...overrides
  }
}

test('actor search params keep list pages and the selected actor apart', () => {
  assert.deepEqual(validateActorsSearch({}), {})
  assert.deepEqual(validateActorsSearch({ page: 2 }), { page: 2 })
  assert.deepEqual(validateActorsSearch({ actor: 5, name: ' 名字 ' }), { actor: 5, name: '名字' })
  assert.deepEqual(validateActorsSearch({ actor: '5', actorPage: '3' }), { actor: 5, actorPage: 3 })
  // A name without an actor target is stale state and must not survive.
  assert.deepEqual(validateActorsSearch({ name: '名字' }), {})
  for (const invalid of [
    { actor: 0 },
    { actor: '-1' },
    { actor: 'abc' },
    { page: 0 },
    { actorPage: 1.5 }
  ]) {
    assert.throws(() => validateActorsSearch(invalid), Error)
  }
})

test('actor cards render the count badge and a fallback without an avatar', () => {
  const html = renderToStaticMarkup(
    <LibraryActorCard actor={actor({ avatar: 'https://c0.example/a.jpg' })} onSelect={() => {}} />
  )
  assert.match(html, /X actor/)
  assert.match(html, /3 部/)
  const plain = renderToStaticMarkup(
    <LibraryActorCard actor={actor({ avatar: undefined })} onSelect={() => {}} />
  )
  assert.match(plain, /X actor/)
  assert.doesNotMatch(plain, /<img/)
})

function renderedPage(ui: ReactNode, cache: Array<[readonly unknown[], unknown]>): string {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  onTestFinished(() => client.clear())
  for (const [key, data] of cache) client.setQueryData(key, data)
  return renderToStaticMarkup(
    <MovieDetailDialogContext value={() => {}}>
      <TooltipProvider>
        <QueryClientProvider client={client}>{ui}</QueryClientProvider>
      </TooltipProvider>
    </MovieDetailDialogContext>
  )
}

const noop = () => {}

test('the actors page lists library actors with their counts', () => {
  const page: LibraryActorsPageData = {
    actors: [
      actor({ name: 'A actor', movie_count: 12 }),
      actor({ id: 6, name: 'B actor', movie_count: 2 })
    ],
    total: 2,
    page: 1,
    has_more: false
  }
  const html = renderedPage(
    <LibraryActorsPage
      page={1}
      actorPage={1}
      onActorsPageChange={noop}
      onSelectActor={noop}
      onActorMoviesPageChange={noop}
      onBackToList={noop}
    />,
    [[libraryKeys.actors(1), page]]
  )
  assert.match(html, /已标记为女性的演员/)
  assert.match(html, /A actor/)
  assert.match(html, /12 部/)
  assert.match(html, /B actor/)
})

test('the selected actor page shows their movies and a way back', () => {
  const movies: LibraryPage = {
    movies: [
      {
        id: 42,
        code: 'ABP-002',
        title: 'Shared movie',
        duration: 0,
        rating: 0,
        actors: [],
        tags: [],
        scrape_status: 'done'
      }
    ],
    total: 1,
    page: 1,
    has_more: false
  }
  const html = renderedPage(
    <LibraryActorsPage
      page={1}
      actor={5}
      actorName="X actor"
      actorPage={1}
      onActorsPageChange={noop}
      onSelectActor={noop}
      onActorMoviesPageChange={noop}
      onBackToList={noop}
    />,
    [[libraryKeys.movies(1, 5), movies]]
  )
  assert.match(html, /X actor/)
  assert.match(html, /1 部影片/)
  assert.match(html, /返回演员列表/)
  assert.match(html, /ABP-002/)
})
