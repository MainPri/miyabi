import { UserRoundIcon } from 'lucide-react'

import { imageURL } from '@/api/client'
import { type LibraryActor } from '@/api/library'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'

export function LibraryActorCard({
  actor,
  onSelect
}: {
  actor: LibraryActor
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className="block h-full w-full rounded-2xl outline-ring"
    >
      <Card size="sm" className="h-full gap-0 hover:bg-muted/40">
        <CardContent className="flex h-full flex-col items-center justify-center gap-2.5 p-4 text-center">
          <Avatar className="size-16">
            {actor.avatar ? (
              <AvatarImage src={imageURL(actor.avatar)} alt={actor.name} loading="lazy" />
            ) : null}
            <AvatarFallback>
              <UserRoundIcon className="size-7" />
            </AvatarFallback>
          </Avatar>
          <h3 className="max-w-full truncate text-sm leading-5 font-semibold">{actor.name}</h3>
          <Badge variant="secondary">{actor.movie_count} 部</Badge>
        </CardContent>
      </Card>
    </button>
  )
}
