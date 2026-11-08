import { MIN_PASSWORD_LENGTH } from './passwordPolicy'
import { ErrorBanner } from './ErrorBanner'
import { useAuthStatus } from './useAuthStatus'

/**
 * Tells an unlocked user that router-manager's own password is shorter than
 * the minimum a new one must meet. A stored hash can't be length-checked, so
 * the backend learns this only when the password is actually typed (see
 * authgate.Gate.WeakPassword) and reports it on /api/auth/status solely to a
 * request that is unlocked - which is why this renders nothing until then,
 * and why it has no dismiss button: it goes away by changing the password.
 *
 * The fix differs by where the hash comes from: an env-pinned one can't be
 * changed through the API, so the user is sent to the host instead.
 * onOpenSettings is omitted where the settings tab isn't reachable (embed).
 */
export function WeakPasswordBanner({ onOpenSettings }: { onOpenSettings?: () => void }) {
  const { status } = useAuthStatus()
  if (!status?.unlocked || !status.weakPassword) return null

  if (status.source === 'env') {
    return (
      <ErrorBanner
        variant="warning"
        message={
          <>
            router 관리자 비밀번호가 {MIN_PASSWORD_LENGTH}자보다 짧습니다. 이 비밀번호는
            ROUTER_MANAGER_AUTH_PASSWORD_HASH로 고정되어 있어 여기서 바꿀 수 없습니다 - 호스트에서{' '}
            <code>migrate.sh</code>를 다시 실행하면 변경을 물어봅니다. 직접 하려면 {MIN_PASSWORD_LENGTH}자 이상
            비밀번호로 <code>router-manager --hash-password</code>를 실행해 나온 해시를 .env.router의
            ROUTER_MANAGER_AUTH_PASSWORD_HASH에 넣고 router를 다시 만드세요.
          </>
        }
      />
    )
  }

  return (
    <ErrorBanner
      variant="warning"
      message={
        <>
          router 관리자 비밀번호가 {MIN_PASSWORD_LENGTH}자보다 짧습니다. 설정 탭의 비밀번호 변경에서{' '}
          {MIN_PASSWORD_LENGTH}자 이상으로 바꿔 주세요.{' '}
          {onOpenSettings && (
            <button type="button" className="btn btn-secondary btn-small" onClick={onOpenSettings}>
              설정 열기
            </button>
          )}
        </>
      }
    />
  )
}
