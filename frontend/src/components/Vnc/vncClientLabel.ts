// Turns a raw User-Agent header into a short "browser / OS" label for the
// connected-clients panel - just enough to tell two rows apart at a glance
// ("이게 내 폰인가, 다른 사람인가"), not a real UA parser. Order matters:
// Edge/Opera/Chrome all carry a "Chrome" token in their own UA string, and
// Chrome itself carries "Safari", so the more specific tokens have to be
// checked first or every non-Chrome Chromium browser would misreport as
// Chrome, and every WebKit browser would misreport as Safari.
function browserName(ua: string): string {
  if (/Edg\//.test(ua)) return 'Edge'
  if (/OPR\//.test(ua)) return 'Opera'
  if (/Chrome\//.test(ua)) return 'Chrome'
  if (/Firefox\//.test(ua)) return 'Firefox'
  if (/Safari\//.test(ua)) return 'Safari'
  return '알 수 없는 브라우저'
}

function osName(ua: string): string {
  if (/Android/.test(ua)) return 'Android'
  if (/iPhone|iPad|iPod/.test(ua)) return 'iOS'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Mac OS X/.test(ua)) return 'macOS'
  if (/Linux/.test(ua)) return 'Linux'
  return '알 수 없는 OS'
}

export function vncClientLabel(userAgent: string): string {
  if (!userAgent) return '알 수 없음'
  return `${browserName(userAgent)} / ${osName(userAgent)}`
}

// "n초 전 접속" / "n분 전 접속" - deliberately coarse (no hours/days
// breakdown) since a VNC session left open that long is already an outlier
// worth investigating in its own right, not something this label needs to
// format nicely.
export function vncConnectedFor(connectedAtIso: string): string {
  const ms = Date.now() - new Date(connectedAtIso).getTime()
  if (ms < 0) return '방금 접속'
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}초 전 접속`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}분 전 접속`
  const hours = Math.floor(minutes / 60)
  return `${hours}시간 전 접속`
}
