package servo

import (
	"errors"
	"fmt"
)

// HTTPStatus is one HTTP response status, usable as an error. A //servo:
// handler steers the generated adapter with it in the error position:
//
//	return nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", c, err)  // 404
//	return servo.JSON(resp), servo.Status.CREATED                        // 201, by design
//
// A 2xx status in the error position is a deliberate part of the contract:
// it is how a handler picks a success code other than 200. The generated
// adapter recovers the status with errors.As and reads Code(); nil means
// plain 200.
//
// The only values are the fields of Status — the zero HTTPStatus is not a
// status and renders as "HTTP 0".
type HTTPStatus struct {
	code int
	text string
}

// Code returns the numeric status code, read by generated adapters.
func (s HTTPStatus) Code() int { return s.code }

// Error returns the canonical RFC 9110 text ("Not Found"), which is also
// what a 5xx response body carries instead of the wrapped detail.
func (s HTTPStatus) Error() string {
	if s.text == "" {
		return fmt.Sprintf("HTTP %d", s.code)
	}
	return s.text
}

// New returns an error carrying s and msg. The message is what a 4xx
// response body shows the client, so write it for the caller of the API.
func (s HTTPStatus) New(msg string) error {
	return statusError{status: s, err: errors.New(msg)}
}

// Newf is New with fmt.Sprintf formatting.
func (s HTTPStatus) Newf(format string, args ...any) error {
	return statusError{status: s, err: fmt.Errorf(format, args...)}
}

// Wrap returns an error carrying s whose message and chain are err's own.
// Wrap(nil) returns the bare status, so a handler can pass through an
// optional cause without checking it first.
func (s HTTPStatus) Wrap(err error) error {
	if err == nil {
		return s
	}
	return statusError{status: s, err: err}
}

// Wrapf is Wrap with fmt.Errorf formatting; %w keeps the cause in the
// chain for errors.Is/errors.As.
func (s HTTPStatus) Wrapf(format string, args ...any) error {
	return statusError{status: s, err: fmt.Errorf(format, args...)}
}

// statusError pairs a status with the user's error so both survive the
// chain: errors.As finds the HTTPStatus, errors.Is finds the cause.
type statusError struct {
	status HTTPStatus
	err    error
}

// Error returns the user's message verbatim — it is what a 4xx response
// body carries.
func (e statusError) Error() string { return e.err.Error() }

func (e statusError) Unwrap() []error { return []error{e.status, e.err} }

// Status is the table of HTTP statuses, named as RFC 9110 (and the IANA
// registry) spells them — which is why 413 is CONTENT_TOO_LARGE and 422 is
// UNPROCESSABLE_CONTENT rather than the pre-9110 names net/http still
// prints. The SCREAMING_SNAKE fields mirror the registry's own names so a
// handler reads like the spec it implements.
var Status = struct {
	// 1xx informational.
	CONTINUE            HTTPStatus
	SWITCHING_PROTOCOLS HTTPStatus
	PROCESSING          HTTPStatus
	EARLY_HINTS         HTTPStatus

	// 2xx success.
	OK                            HTTPStatus
	CREATED                       HTTPStatus
	ACCEPTED                      HTTPStatus
	NON_AUTHORITATIVE_INFORMATION HTTPStatus
	NO_CONTENT                    HTTPStatus
	RESET_CONTENT                 HTTPStatus
	PARTIAL_CONTENT               HTTPStatus
	MULTI_STATUS                  HTTPStatus
	ALREADY_REPORTED              HTTPStatus
	IM_USED                       HTTPStatus

	// 3xx redirection.
	MULTIPLE_CHOICES   HTTPStatus
	MOVED_PERMANENTLY  HTTPStatus
	FOUND              HTTPStatus
	SEE_OTHER          HTTPStatus
	NOT_MODIFIED       HTTPStatus
	USE_PROXY          HTTPStatus
	TEMPORARY_REDIRECT HTTPStatus
	PERMANENT_REDIRECT HTTPStatus

	// 4xx client errors.
	BAD_REQUEST                     HTTPStatus
	UNAUTHORIZED                    HTTPStatus
	PAYMENT_REQUIRED                HTTPStatus
	FORBIDDEN                       HTTPStatus
	NOT_FOUND                       HTTPStatus
	METHOD_NOT_ALLOWED              HTTPStatus
	NOT_ACCEPTABLE                  HTTPStatus
	PROXY_AUTHENTICATION_REQUIRED   HTTPStatus
	REQUEST_TIMEOUT                 HTTPStatus
	CONFLICT                        HTTPStatus
	GONE                            HTTPStatus
	LENGTH_REQUIRED                 HTTPStatus
	PRECONDITION_FAILED             HTTPStatus
	CONTENT_TOO_LARGE               HTTPStatus
	URI_TOO_LONG                    HTTPStatus
	UNSUPPORTED_MEDIA_TYPE          HTTPStatus
	RANGE_NOT_SATISFIABLE           HTTPStatus
	EXPECTATION_FAILED              HTTPStatus
	IM_A_TEAPOT                     HTTPStatus
	MISDIRECTED_REQUEST             HTTPStatus
	UNPROCESSABLE_CONTENT           HTTPStatus
	LOCKED                          HTTPStatus
	FAILED_DEPENDENCY               HTTPStatus
	TOO_EARLY                       HTTPStatus
	UPGRADE_REQUIRED                HTTPStatus
	PRECONDITION_REQUIRED           HTTPStatus
	TOO_MANY_REQUESTS               HTTPStatus
	REQUEST_HEADER_FIELDS_TOO_LARGE HTTPStatus
	UNAVAILABLE_FOR_LEGAL_REASONS   HTTPStatus

	// 5xx server errors.
	INTERNAL_SERVER_ERROR           HTTPStatus
	NOT_IMPLEMENTED                 HTTPStatus
	BAD_GATEWAY                     HTTPStatus
	SERVICE_UNAVAILABLE             HTTPStatus
	GATEWAY_TIMEOUT                 HTTPStatus
	HTTP_VERSION_NOT_SUPPORTED      HTTPStatus
	VARIANT_ALSO_NEGOTIATES         HTTPStatus
	INSUFFICIENT_STORAGE            HTTPStatus
	LOOP_DETECTED                   HTTPStatus
	NOT_EXTENDED                    HTTPStatus
	NETWORK_AUTHENTICATION_REQUIRED HTTPStatus
}{
	CONTINUE:            HTTPStatus{100, "Continue"},
	SWITCHING_PROTOCOLS: HTTPStatus{101, "Switching Protocols"},
	PROCESSING:          HTTPStatus{102, "Processing"},
	EARLY_HINTS:         HTTPStatus{103, "Early Hints"},

	OK:                            HTTPStatus{200, "OK"},
	CREATED:                       HTTPStatus{201, "Created"},
	ACCEPTED:                      HTTPStatus{202, "Accepted"},
	NON_AUTHORITATIVE_INFORMATION: HTTPStatus{203, "Non-Authoritative Information"},
	NO_CONTENT:                    HTTPStatus{204, "No Content"},
	RESET_CONTENT:                 HTTPStatus{205, "Reset Content"},
	PARTIAL_CONTENT:               HTTPStatus{206, "Partial Content"},
	MULTI_STATUS:                  HTTPStatus{207, "Multi-Status"},
	ALREADY_REPORTED:              HTTPStatus{208, "Already Reported"},
	IM_USED:                       HTTPStatus{226, "IM Used"},

	MULTIPLE_CHOICES:   HTTPStatus{300, "Multiple Choices"},
	MOVED_PERMANENTLY:  HTTPStatus{301, "Moved Permanently"},
	FOUND:              HTTPStatus{302, "Found"},
	SEE_OTHER:          HTTPStatus{303, "See Other"},
	NOT_MODIFIED:       HTTPStatus{304, "Not Modified"},
	USE_PROXY:          HTTPStatus{305, "Use Proxy"},
	TEMPORARY_REDIRECT: HTTPStatus{307, "Temporary Redirect"},
	PERMANENT_REDIRECT: HTTPStatus{308, "Permanent Redirect"},

	BAD_REQUEST:                     HTTPStatus{400, "Bad Request"},
	UNAUTHORIZED:                    HTTPStatus{401, "Unauthorized"},
	PAYMENT_REQUIRED:                HTTPStatus{402, "Payment Required"},
	FORBIDDEN:                       HTTPStatus{403, "Forbidden"},
	NOT_FOUND:                       HTTPStatus{404, "Not Found"},
	METHOD_NOT_ALLOWED:              HTTPStatus{405, "Method Not Allowed"},
	NOT_ACCEPTABLE:                  HTTPStatus{406, "Not Acceptable"},
	PROXY_AUTHENTICATION_REQUIRED:   HTTPStatus{407, "Proxy Authentication Required"},
	REQUEST_TIMEOUT:                 HTTPStatus{408, "Request Timeout"},
	CONFLICT:                        HTTPStatus{409, "Conflict"},
	GONE:                            HTTPStatus{410, "Gone"},
	LENGTH_REQUIRED:                 HTTPStatus{411, "Length Required"},
	PRECONDITION_FAILED:             HTTPStatus{412, "Precondition Failed"},
	CONTENT_TOO_LARGE:               HTTPStatus{413, "Content Too Large"},
	URI_TOO_LONG:                    HTTPStatus{414, "URI Too Long"},
	UNSUPPORTED_MEDIA_TYPE:          HTTPStatus{415, "Unsupported Media Type"},
	RANGE_NOT_SATISFIABLE:           HTTPStatus{416, "Range Not Satisfiable"},
	EXPECTATION_FAILED:              HTTPStatus{417, "Expectation Failed"},
	IM_A_TEAPOT:                     HTTPStatus{418, "I'm a teapot"},
	MISDIRECTED_REQUEST:             HTTPStatus{421, "Misdirected Request"},
	UNPROCESSABLE_CONTENT:           HTTPStatus{422, "Unprocessable Content"},
	LOCKED:                          HTTPStatus{423, "Locked"},
	FAILED_DEPENDENCY:               HTTPStatus{424, "Failed Dependency"},
	TOO_EARLY:                       HTTPStatus{425, "Too Early"},
	UPGRADE_REQUIRED:                HTTPStatus{426, "Upgrade Required"},
	PRECONDITION_REQUIRED:           HTTPStatus{428, "Precondition Required"},
	TOO_MANY_REQUESTS:               HTTPStatus{429, "Too Many Requests"},
	REQUEST_HEADER_FIELDS_TOO_LARGE: HTTPStatus{431, "Request Header Fields Too Large"},
	UNAVAILABLE_FOR_LEGAL_REASONS:   HTTPStatus{451, "Unavailable For Legal Reasons"},

	INTERNAL_SERVER_ERROR:           HTTPStatus{500, "Internal Server Error"},
	NOT_IMPLEMENTED:                 HTTPStatus{501, "Not Implemented"},
	BAD_GATEWAY:                     HTTPStatus{502, "Bad Gateway"},
	SERVICE_UNAVAILABLE:             HTTPStatus{503, "Service Unavailable"},
	GATEWAY_TIMEOUT:                 HTTPStatus{504, "Gateway Timeout"},
	HTTP_VERSION_NOT_SUPPORTED:      HTTPStatus{505, "HTTP Version Not Supported"},
	VARIANT_ALSO_NEGOTIATES:         HTTPStatus{506, "Variant Also Negotiates"},
	INSUFFICIENT_STORAGE:            HTTPStatus{507, "Insufficient Storage"},
	LOOP_DETECTED:                   HTTPStatus{508, "Loop Detected"},
	NOT_EXTENDED:                    HTTPStatus{510, "Not Extended"},
	NETWORK_AUTHENTICATION_REQUIRED: HTTPStatus{511, "Network Authentication Required"},
}
