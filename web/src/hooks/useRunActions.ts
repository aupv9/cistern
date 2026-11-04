import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { invalidateAfterAction, runBackup, runDrill, runVerify } from '~/api/queries'

export function useRunBackup() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (job: string) => runBackup(job),
    onSuccess: (ref, job) => {
      invalidateAfterAction(queryClient, job)
      void navigate({ to: '/runs/$run', params: { run: ref.run_id } })
    },
  })
}

export function useRunVerify() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ job, snapshot }: { job: string; snapshot?: string }) => runVerify(job, snapshot),
    onSuccess: (ref, { job }) => {
      invalidateAfterAction(queryClient, job)
      void navigate({ to: '/runs/$run', params: { run: ref.run_id } })
    },
  })
}

export function useRunDrill() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ job, snapshot }: { job: string; snapshot?: string }) => runDrill(job, snapshot),
    onSuccess: (ref, { job }) => {
      invalidateAfterAction(queryClient, job)
      void navigate({ to: '/runs/$run', params: { run: ref.run_id } })
    },
  })
}
