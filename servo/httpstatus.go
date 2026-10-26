package servo

import (
	"errors"
	"fmt"
)

// HTTPStatus is one HTTP response status, usable as an error. A //servo:
// handler steers the generated adapter with it in the error position:
//
//	return nil, servo.Status.NOT_FOUND.Newf("no category %q", c)  // 404, that message
//	return nil, servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)      // 500, err logged
//	return servo.JSON(resp), servo.Status.CREATED                 // 201, by design
//
// A 2xx status in the error position is a deliberate part of the contract:
// it is how a handler picks a success code other than 200. The generated
// adapter recovers the status with errors.As and reads Code(); nil means
// plain 200.
//
// What the client reads is decided by the code. Under a 4xx status the
// response body is the returned error's Error() — all of it, including the
// text of any error that Wrap, Wrapf or a %w verb pulled into the chain —
// so a 4xx error must be one whose every word was written for the API's
// caller. Under a 5xx status the body is the canonical text alone ("Internal
// Server Error") and the full error goes to the log.
//
// The values come from the Status table, or from StatusFor for a code the
// table leaves out. The zero HTTPStatus is not a status: it renders as
// "HTTP 0", and the generated adapter answers 500 for it, as for any code
// outside 100–999.
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
// It is the constructor to reach for under a 4xx status: the client reads
// exactly what you wrote and nothing else.
func (s HTTPStatus) New(msg string) error {
	return statusError{status: s, err: errors.New(msg)}
}

// Newf is New with fmt.Sprintf formatting. A %w verb works as it does in
// fmt.Errorf, and brings the wrapped error's text into the message with
// it — which under a 4xx status is what the client reads.
func (s HTTPStatus) Newf(format string, args ...any) error {
	return statusError{status: s, err: fmt.Errorf(format, args...)}
}

// Wrap returns an error carrying s whose message and chain are err's own.
// Wrap(nil) returns the bare status, so a handler can pass through an
// optional cause without checking it first.
//
// Under a 4xx status err's message is the response body, so wrap only an
// error whose every layer was written for the API's caller — a domain
// validation error, say. An error from a driver, a client library or the
// filesystem names tables, constraints, hosts and paths: answer it with
// New or Newf and a message of your own, and log the cause yourself if it
// matters. Under a 5xx status Wrap is always safe: the client reads only
// the canonical text, and the generated adapter logs err.
func (s HTTPStatus) Wrap(err error) error {
	if err == nil {
		return s
	}
	return statusError{status: s, err: err}
}

// Wrapf is Wrap with fmt.Errorf formatting; %w keeps the cause in the
// chain for errors.Is/errors.As — and its text in the message, which under
// a 4xx status is what the client reads (see Wrap).
func (s HTTPStatus) Wrapf(format string, args ...any) error {
	return statusError{status: s, err: fmt.Errorf(format, args...)}
}

// StatusFor returns the HTTPStatus for code: the Status table's own entry
// when there is one, so StatusFor(404) == Status.NOT_FOUND, and otherwise
// a status carrying code whose text is "HTTP <code>" — for the codes the
// table leaves out, such as nginx's 499, or whatever an upstream service
// answered:
//
//	return nil, servo.StatusFor(resp.StatusCode).Newf("upstream answered %d", resp.StatusCode)
//
// A code outside 100–999 is no HTTP status at all; StatusFor returns it as
// given, and the generated adapter answers 500 for it.
func StatusFor(code int) HTTPStatus {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return HTTPStatus{code: code}
}

// statusError pairs a status with the user's error so both survive the
// chain: errors.As finds the HTTPStatus, errors.Is finds the cause.
type statusError struct {
	status HTTPStatus
	err    error
}

// Error returns the user's message verbatim, every wrapped layer included —
// it is what a 4xx response body carries.
func (e statusError) Error() string { return e.err.Error() }

func (e statusError) Unwrap() []error { return []error{e.status, e.err} }

// Status is the table of HTTP statuses, named as RFC 9110 (and the IANA
// registry) spells them — which is why 413 is CONTENT_TOO_LARGE and 422 is
// UNPROCESSABLE_CONTENT rather than the pre-9110 names net/http still
// prints. The SCREAMING_SNAKE fields mirror the registry's own names so a
// handler reads like the spec it implements.
//
// It is a table to read, never to assign to. Go cannot make a struct
// variable read-only, so `servo.Status.NOT_FOUND = …` compiles — and
// changes what every handler and adapter in the process means by that
// name. For a code the table leaves out, use StatusFor.
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

// statusByCode is StatusFor's index into the table. The table is a struct,
// so it is listed again here rather than walked: the runtime does not use
// reflect, and TestStatusForCoversTheTable keeps the two lists in step.
var statusByCode = func() map[int]HTTPStatus {
	all := []HTTPStatus{
		Status.CONTINUE, Status.SWITCHING_PROTOCOLS, Status.PROCESSING,
		Status.EARLY_HINTS, Status.OK, Status.CREATED, Status.ACCEPTED,
		Status.NON_AUTHORITATIVE_INFORMATION, Status.NO_CONTENT,
		Status.RESET_CONTENT, Status.PARTIAL_CONTENT, Status.MULTI_STATUS,
		Status.ALREADY_REPORTED, Status.IM_USED, Status.MULTIPLE_CHOICES,
		Status.MOVED_PERMANENTLY, Status.FOUND, Status.SEE_OTHER,
		Status.NOT_MODIFIED, Status.USE_PROXY, Status.TEMPORARY_REDIRECT,
		Status.PERMANENT_REDIRECT, Status.BAD_REQUEST, Status.UNAUTHORIZED,
		Status.PAYMENT_REQUIRED, Status.FORBIDDEN, Status.NOT_FOUND,
		Status.METHOD_NOT_ALLOWED, Status.NOT_ACCEPTABLE,
		Status.PROXY_AUTHENTICATION_REQUIRED, Status.REQUEST_TIMEOUT,
		Status.CONFLICT, Status.GONE, Status.LENGTH_REQUIRED,
		Status.PRECONDITION_FAILED, Status.CONTENT_TOO_LARGE, Status.URI_TOO_LONG,
		Status.UNSUPPORTED_MEDIA_TYPE, Status.RANGE_NOT_SATISFIABLE,
		Status.EXPECTATION_FAILED, Status.IM_A_TEAPOT, Status.MISDIRECTED_REQUEST,
		Status.UNPROCESSABLE_CONTENT, Status.LOCKED, Status.FAILED_DEPENDENCY,
		Status.TOO_EARLY, Status.UPGRADE_REQUIRED, Status.PRECONDITION_REQUIRED,
		Status.TOO_MANY_REQUESTS, Status.REQUEST_HEADER_FIELDS_TOO_LARGE,
		Status.UNAVAILABLE_FOR_LEGAL_REASONS, Status.INTERNAL_SERVER_ERROR,
		Status.NOT_IMPLEMENTED, Status.BAD_GATEWAY, Status.SERVICE_UNAVAILABLE,
		Status.GATEWAY_TIMEOUT, Status.HTTP_VERSION_NOT_SUPPORTED,
		Status.VARIANT_ALSO_NEGOTIATES, Status.INSUFFICIENT_STORAGE,
		Status.LOOP_DETECTED, Status.NOT_EXTENDED,
		Status.NETWORK_AUTHENTICATION_REQUIRED,
	}
	m := make(map[int]HTTPStatus, len(all))
	for _, s := range all {
		m[s.code] = s
	}
	return m
}()
