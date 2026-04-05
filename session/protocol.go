package session

// Protocol - automation protocol handled by the session.
type Protocol string

const (
	ProtocolWebDriver  Protocol = "webdriver"
	ProtocolPlaywright Protocol = "playwright"
)
