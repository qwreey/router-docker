// Mirrors authgate.MinPasswordLength / CheckNewPassword on the backend, which
// stays authoritative - this only saves a round trip. Counted in code points
// (Array.from iterates by code point, `.length` would count UTF-16 units),
// and nothing else is checked: no composition rules.
export const MIN_PASSWORD_LENGTH = 12

export function isPasswordLongEnough(password: string): boolean {
  return Array.from(password).length >= MIN_PASSWORD_LENGTH
}

export const PASSWORD_TOO_SHORT_MESSAGE = `비밀번호는 ${MIN_PASSWORD_LENGTH}자 이상이어야 합니다`
