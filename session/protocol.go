// Modified by [Aleksander R], 2026: added Playwright protocol support

package session

// Protocol - automation protocol handled by the session.
type Protocol string

const (
	ProtocolWebDriver  Protocol = "webdriver"
	ProtocolPlaywright Protocol = "playwright"
)
