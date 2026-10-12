import { ArrowLeftIcon } from 'lucide-react'

import {
  ACTORS_PAGE_SIZE,
  LIBRARY_PAGE_SIZE,
  useLibraryActors,
  useLibraryMovies
} from '@/api/library'
import { AppPage } from '@/components/app-page'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { ListPagination } from '@/components/list-pagination'
import { MovieGridLayout } from '@/components/movie/movie-grid'
import { MovieGridSkeleton } from '@/components/movie/movie-grid-skeleton'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { LibraryActorCard } from '@/features/library/actor-card'
import { LibraryMovieCard } from '@/features/library/movie-card'

export function LibraryActorsPage({
  page,
  actor,
  actorName,
  actorPage,
  onActorsPageChange,
  onSelectActor,
  onActorMoviesPageChange,
  onBackToList
}: {
  page: number
  actor?: number
  actorName?: string
  actorPage: number
  onActorsPageChange: (page: number) => void
  onSelectActor: (id: number, name: string) => void
  onActorMoviesPageChange: (page: number) => void
  onBackToList: () => void
}) {
  // Both hooks stay unconditional so switching views keeps the hook order stable.
  const actors = useLibraryActors(page)
  const movies = useLibraryMovies(actorPage, actor)

  if (actor === undefined) {
    return (
      <AppPage>
        <PageHeader title="女演员" description="媒体库中已标记为女性的演员，按出演影片数排序" />
        {actors.isPending ? (
          <MovieGridSkeleton count={ACTORS_PAGE_SIZE} compact />
        ) : actors.isError ? (
          <ErrorState
            message="无法读取演员列表，请检查后端服务后重试"
            onRetry={() => void actors.refetch()}
            retrying={actors.isFetching}
          />
        ) : actors.data.actors.length > 0 ? (
          <>
            <MovieGridLayout>
              {actors.data.actors.map(person => (
                <LibraryActorCard
                  key={person.id}
                  actor={person}
                  onSelect={() => onSelectActor(person.id, person.name)}
                />
              ))}
            </MovieGridLayout>
            <ListPagination
              page={page}
              totalPages={Math.max(1, Math.ceil(actors.data.total / ACTORS_PAGE_SIZE))}
              hasMore={actors.data.has_more}
              disabled={actors.isFetching}
              onPageChange={onActorsPageChange}
            />
          </>
        ) : (
          <EmptyState
            className="min-h-0 flex-1 py-12"
            title="还没有已标记为女性的演员"
            actions={
              <span className="text-xs text-muted-foreground">
                影片刮削完成并标记演员性别后，会自动归档到这里
              </span>
            }
          />
        )}
      </AppPage>
    )
  }

  return (
    <AppPage>
      <PageHeader
        title={actorName ?? '演员影片'}
        description={movies.isSuccess ? `${movies.data.total} 部影片` : '该演员在媒体库中的影片'}
        inlineActions
      >
        <Button variant="outline" size="sm" onClick={onBackToList}>
          <ArrowLeftIcon className="size-3.5" />
          返回演员列表
        </Button>
      </PageHeader>
      {movies.isPending ? (
        <MovieGridSkeleton count={LIBRARY_PAGE_SIZE} compact />
      ) : movies.isError ? (
        <ErrorState
          message="无法读取该演员的影片，请检查后端服务后重试"
          onRetry={() => void movies.refetch()}
          retrying={movies.isFetching}
        />
      ) : movies.data.movies.length > 0 ? (
        <>
          <MovieGridLayout>
            {movies.data.movies.map(movie => (
              <LibraryMovieCard key={movie.id} movie={movie} />
            ))}
          </MovieGridLayout>
          <ListPagination
            page={actorPage}
            totalPages={Math.max(1, Math.ceil(movies.data.total / LIBRARY_PAGE_SIZE))}
            hasMore={movies.data.has_more}
            disabled={movies.isFetching}
            onPageChange={onActorMoviesPageChange}
          />
        </>
      ) : (
        <EmptyState className="min-h-0 flex-1 py-12" title="该演员暂时没有可展示的影片" />
      )}
    </AppPage>
  )
}
