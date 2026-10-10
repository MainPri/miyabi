import { FolderMinusIcon, LoaderCircleIcon, RefreshCwIcon } from 'lucide-react'

import { describeApiError } from '@/api/client'
import { useStartLibraryScan } from '@/api/library'
import { useRemovePanDirectory, type PanDirectory } from '@/api/pan'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { notifyTaskError, useNotifyScanTask } from '@/features/tasks/task-toast'

export function PanDirectoryRow({
  accountID,
  directory,
  disabled
}: {
  accountID: string
  directory: PanDirectory
  disabled: boolean
}) {
  const remove = useRemovePanDirectory(accountID)
  const startScan = useStartLibraryScan()
  const scanning = startScan.isPending && startScan.variables?.directoryID === directory.id
  const notifyScan = useNotifyScanTask()

  return (
    <div className="flex items-center justify-between gap-4">
      <div className="min-w-0">
        <div className="truncate text-sm font-medium">{directory.name}</div>
        <div className="truncate text-xs text-muted-foreground">{directory.path}</div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="icon"
              disabled={disabled || scanning}
              onClick={() =>
                startScan.mutate(
                  { rebuild: false, directoryID: directory.id },
                  {
                    onSuccess: tasks => {
                      const latest = tasks[tasks.length - 1]
                      if (latest) notifyScan(latest)
                    },
                    onError: error => {
                      notifyTaskError(
                        `scan:${directory.id}`,
                        '无法创建同步任务',
                        describeApiError(error)
                      )
                    }
                  }
                )
              }
            >
              {scanning ? (
                <LoaderCircleIcon className="size-4 animate-spin" />
              ) : (
                <RefreshCwIcon className="size-4" />
              )}
            </Button>
          </TooltipTrigger>
          <TooltipContent>扫描该目录</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="icon"
              disabled={disabled || remove.isPending}
              onClick={() => remove.mutate(directory.id)}
            >
              {remove.isPending ? (
                <LoaderCircleIcon className="size-4 animate-spin" />
              ) : (
                <FolderMinusIcon className="size-4" />
              )}
            </Button>
          </TooltipTrigger>
          <TooltipContent>取消挂载</TooltipContent>
        </Tooltip>
      </div>
      {remove.isError ? <InlineError>取消挂载未完成，请检查后端服务后重试。</InlineError> : null}
    </div>
  )
}
