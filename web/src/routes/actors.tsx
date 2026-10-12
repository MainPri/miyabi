import { createFileRoute } from '@tanstack/react-router'

import { AppPage } from '@/components/app-page'
import { ErrorState } from '@/components/error-state'
import { LibraryActorsPage } from '@/features/library/actors-page'
import { validateActorsSearch } from '@/features/library/search'

export const Route = createFileRoute('/actors')({
  validateSearch: validateActorsSearch,
  component: ActorsRoute,
  errorComponent: () => (
    <AppPage>
      <ErrorState message="演员页参数无效" />
    </AppPage>
  )
})

function ActorsRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <LibraryActorsPage
      page={search.page ?? 1}
      actor={search.actor}
      actorName={search.name}
      actorPage={search.actorPage ?? 1}
      onActorsPageChange={p =>
        void navigate({ search: prev => ({ ...prev, page: p > 1 ? p : undefined }) })
      }
      onSelectActor={(id, name) => void navigate({ search: () => ({ actor: id, name }) })}
      onActorMoviesPageChange={p =>
        void navigate({ search: prev => ({ ...prev, actorPage: p > 1 ? p : undefined }) })
      }
      onBackToList={() => void navigate({ search: prev => ({ page: prev.page }) })}
    />
  )
}
