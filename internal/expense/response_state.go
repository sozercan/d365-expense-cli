package expense

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// mergeSameOriginResponseState applies only state returned by the configured
// Dynamics origin. Server-only ms-dyn-* headers are not copied into future
// requests unless the corresponding request header was already captured.
func mergeSameOriginResponseState(origin string, headers http.Header, cookies *[]*http.Cookie, response *http.Response) {
	if headers == nil || cookies == nil || !responseMatchesOrigin(response, origin) {
		return
	}

	for name, values := range response.Header {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "ms-dyn-") {
			continue
		}
		destinationName, ok := existingHeaderName(headers, name)
		if !ok || len(values) == 0 {
			continue
		}
		delete(headers, destinationName)
		for _, value := range values {
			headers.Add(destinationName, value)
		}
	}

	*cookies = mergeResponseCookies(*cookies, response.Cookies(), response.Request.URL)
}

func responseMatchesOrigin(response *http.Response, origin string) bool {
	if response == nil || response.Request == nil || response.Request.URL == nil {
		return false
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" {
		return false
	}
	return strings.EqualFold(parsedOrigin.Scheme, response.Request.URL.Scheme) &&
		strings.EqualFold(parsedOrigin.Host, response.Request.URL.Host)
}

func existingHeaderName(headers http.Header, wanted string) (string, bool) {
	for name := range headers {
		if strings.EqualFold(name, wanted) {
			return name, true
		}
	}
	return "", false
}

func mergeResponseCookies(current, updates []*http.Cookie, requestURL *url.URL) []*http.Cookie {
	if requestURL == nil || len(updates) == 0 {
		return current
	}

	result := cloneCookies(current)
	for _, update := range updates {
		if update == nil || strings.TrimSpace(update.Name) == "" || !cookieDomainMatchesHost(update.Domain, requestURL.Hostname()) {
			continue
		}
		clone := *update
		if clone.Path == "" {
			clone.Path = defaultCookiePath(requestURL.Path)
		}
		identity := cookieIdentity(&clone, requestURL)
		index := -1
		for candidateIndex, candidate := range result {
			if cookieIdentity(candidate, requestURL) == identity {
				index = candidateIndex
				break
			}
		}

		remove := clone.MaxAge < 0 || (!clone.Expires.IsZero() && clone.Expires.Before(time.Now()))
		if remove {
			if index >= 0 {
				result = append(result[:index], result[index+1:]...)
			}
			continue
		}
		if index >= 0 {
			result[index] = &clone
		} else {
			result = append(result, &clone)
		}
	}
	return result
}

func cookieIdentity(cookie *http.Cookie, requestURL *url.URL) string {
	if cookie == nil {
		return ""
	}
	domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
	if domain == "" && requestURL != nil {
		domain = strings.ToLower(requestURL.Hostname())
	}
	path := cookie.Path
	if path == "" && requestURL != nil {
		path = defaultCookiePath(requestURL.Path)
	}
	return cookie.Name + "\x00" + domain + "\x00" + path
}

func cookieDomainMatchesHost(domain, host string) bool {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	host = strings.ToLower(strings.TrimSpace(host))
	return domain == "" || host == domain || strings.HasSuffix(host, "."+domain)
}

func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' || requestPath == "/" {
		return "/"
	}
	lastSlash := strings.LastIndex(requestPath, "/")
	if lastSlash <= 0 {
		return "/"
	}
	return requestPath[:lastSlash]
}
