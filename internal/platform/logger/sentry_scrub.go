package logger

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/getsentry/sentry-go"
)

const scrubbed = "[redacted]"

var (
	// URLs keep scheme, host and path; query strings and fragments (presigned signatures, OAuth codes) go.
	urlPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)
	// "Bearer <token>", "Basic <creds>".
	authSchemePattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	// key=value or "key": "value" where the key names a secret.
	secretPairPattern = regexp.MustCompile(`(?i)("?(?:[a-z0-9_-]*(?:token|secret|password|signature|credential|api[_-]?key|authorization|code_verifier)[a-z0-9_-]*|x-amz-[a-z-]+)"?\s*[:=]\s*"?)([^\s"&,;}]+)`)
	// JSON Web Tokens and long opaque strings that look like keys (hex ≥ 32, base64url ≥ 40).
	tokenPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\b[0-9a-fA-F]{32,}\b|\b[A-Za-z0-9_-]{40,}\b`)
)

var sensitiveHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true,
	"x-telegram-bot-api-secret-token": true, "x-api-key": true, "x-amz-security-token": true,
}

// scrubSentryEvent is Sentry's BeforeSend: it strips query strings from URLs, auth headers, cookies and anything
// that looks like a presigned URL, token or key from every text the event carries (security.md §1 row 7).
func scrubSentryEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	event.Message = scrubText(event.Message)
	event.Transaction = scrubText(event.Transaction)
	for i := range event.Exception {
		event.Exception[i].Value = scrubText(event.Exception[i].Value)
	}
	for key, value := range event.Tags {
		event.Tags[key] = scrubText(value)
	}
	for name, context := range event.Contexts {
		for key, value := range context {
			context[key] = scrubValue(value)
		}
		event.Contexts[name] = context
	}
	for _, crumb := range event.Breadcrumbs {
		if crumb == nil {
			continue
		}
		crumb.Message = scrubText(crumb.Message)
		for key, value := range crumb.Data {
			crumb.Data[key] = scrubValue(value)
		}
	}
	if request := event.Request; request != nil {
		request.URL = scrubURL(request.URL)
		request.QueryString = ""
		request.Cookies = ""
		request.Data = scrubText(request.Data)
		for key, value := range request.Headers {
			if sensitiveHeaders[strings.ToLower(key)] {
				request.Headers[key] = scrubbed
				continue
			}
			request.Headers[key] = scrubText(value)
		}
		request.Env = nil
	}
	return event
}

func scrubValue(value any) any {
	switch v := value.(type) {
	case string:
		return scrubText(v)
	case error:
		return scrubText(v.Error())
	case map[string]any:
		for key, item := range v {
			v[key] = scrubValue(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = scrubValue(item)
		}
		return v
	case []string:
		for i, item := range v {
			v[i] = scrubText(item)
		}
		return v
	default:
		return value
	}
}

func scrubText(text string) string {
	if text == "" {
		return text
	}
	text = urlPattern.ReplaceAllStringFunc(text, scrubURL)
	text = authSchemePattern.ReplaceAllString(text, "$1 "+scrubbed)
	text = secretPairPattern.ReplaceAllString(text, "${1}"+scrubbed)
	return tokenPattern.ReplaceAllString(text, scrubbed)
}

func scrubURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			return raw[:i]
		}
		return raw
	}
	parsed.RawQuery, parsed.Fragment, parsed.RawFragment, parsed.User = "", "", "", nil
	parsed.ForceQuery = false
	return parsed.String()
}
