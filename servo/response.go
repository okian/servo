package servo

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
)

// Response is what a //servo: handler's first result must satisfy: a value
// that knows how to write itself as one HTTP response body. It is sealed —
// the write method is unexported — so every value came from one of this
// package's constructors (JSON, XML, Text, HTML, Blob, Redirect, Stream)
// and the generated adapter can hand any of them to WriteResponse.
//
// It is an interface rather than a struct so the error path can return a
// plain nil; a nil Response writes the status code and nothing else, which
// is also the 204 idiom: `return nil, servo.Status.NO_CONTENT`.
type Response interface {
	// write sets the content type, writes code, then the body. A kind
	// that can fail before the body exists — an encoder — fails before
	// writing code too, and answers with internalError instead.
	write(w http.ResponseWriter, code int) error
}

// WriteResponse writes res with the given status code. It exists because
// the generated adapters live in the user's package, where the sealed write
// method is unreachable. A nil res writes the code alone.
//
// What an error leaves behind depends on how far the write got. When it
// failed before anything was written — a JSON or XML payload that does not
// encode (a NaN, a channel, a MarshalJSON that fails), or a code that is
// no HTTP status at all — WriteResponse has already answered 500 with the
// body the generated adapters give every other server error,
// {"error": "Internal Server Error"}, and the error carries
// Status.INTERNAL_SERVER_ERROR, so errors.As tells the caller the exchange
// is complete and only the log is left to write. Any other error means
// the status line is out and the body is not whole: a write that failed,
// or a Stream whose reader failed partway. All a caller can do then is
// make sure the client cannot mistake the cut-short body for a complete
// one, which the generated adapter does by breaking the connection.
func WriteResponse(w http.ResponseWriter, code int, res Response) error {
	if code < 100 || code > 999 {
		// net/http panics on a code like this. The adapters generated
		// since v3.5.0 never pass one, but an older one hands a zero
		// HTTPStatus's code straight through, and a 500 is what it
		// means.
		return internalError(w, fmt.Errorf("servo: %d is not an HTTP status code", code))
	}
	if res == nil {
		w.WriteHeader(code)
		return nil
	}
	return res.write(w, code)
}

// internalError answers a response that cannot be written as asked with
// the 500 the generated adapters write for any other server error — the
// same envelope, the canonical text — and returns cause carrying
// Status.INTERNAL_SERVER_ERROR. Nothing has been written when it is
// called, which is the point: the client gets a whole, truthful response
// instead of a success status over an empty or partial body.
func internalError(w http.ResponseWriter, cause error) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, "{\"error\":\"Internal Server Error\"}\n")
	return Status.INTERNAL_SERVER_ERROR.Wrap(cause)
}

// Xml is Json's encoding/xml sibling: the typed response wrapper an XML
// handler declares, constructed only by XML.
type Xml[T any] interface {
	Response
	// Value returns the payload, mostly for tests — encoding happens
	// through WriteResponse.
	Value() T
}

// XML wraps a payload for encoding as application/xml. Spelled XML for the
// same reason JSON is: the type owns Xml.
func XML[T any](v T) Xml[T] {
	return xmlValue[T]{v: v}
}

type xmlValue[T any] struct{ v T }

func (x xmlValue[T]) Value() T { return x.v }

func (x xmlValue[T]) write(w http.ResponseWriter, code int) error {
	// Encoded in full before the status goes out, for the reason
	// jsonValue's write gives — and encoding/xml writes as it goes, so
	// encoding straight into w could also leave half a document behind.
	var body bytes.Buffer
	if err := xml.NewEncoder(&body).Encode(x.v); err != nil {
		return internalError(w, fmt.Errorf("servo: encoding the XML response: %w", err))
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, err := w.Write(body.Bytes())
	return err
}

// Text responds with a text/plain body.
func Text(s string) Response { return textResponse(s) }

type textResponse string

func (t textResponse) write(w http.ResponseWriter, code int) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, err := io.WriteString(w, string(t))
	return err
}

// HTML responds with a text/html body.
func HTML(s string) Response { return htmlResponse(s) }

type htmlResponse string

func (h htmlResponse) write(w http.ResponseWriter, code int) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, err := io.WriteString(w, string(h))
	return err
}

// Blob responds with raw bytes under the given content type; empty means
// application/octet-stream.
func Blob(contentType string, data []byte) Response {
	return blobResponse{contentType: contentType, data: data}
}

type blobResponse struct {
	contentType string
	data        []byte
}

func (b blobResponse) write(w http.ResponseWriter, code int) error {
	ct := b.contentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(code)
	_, err := w.Write(b.data)
	return err
}

// Redirect responds with a Location header and no body. The status decides
// the redirect kind, so pair it with one in the error position:
//
//	return servo.Redirect("/new/place"), servo.Status.FOUND
//
// A 3xx status flows through the success path like a 2xx does.
func Redirect(url string) Response { return redirectResponse(url) }

type redirectResponse string

func (r redirectResponse) write(w http.ResponseWriter, code int) error {
	w.Header().Set("Location", string(r))
	w.WriteHeader(code)
	return nil
}

// Stream copies r as the response body — files, pipes, an upstream
// response's body, anything io.Copy can drain. Empty contentType means
// application/octet-stream.
//
// The response owns r from the moment the handler returns it: when the
// copy ends, however it ends, r is closed if it implements io.Closer,
// which is what gives back a file's descriptor or an upstream response's
// pooled connection. So do not close it yourself — a deferred Close in the
// handler runs before the copy starts and leaves nothing to read. Only a
// Stream that is written gets closed, and one returned beside an error
// status (400 and up) never is: on an error path, close the reader and
// return nil.
//
// The copy runs after the status line is committed, so a reader that
// fails partway can no longer turn the response into an error.
// WriteResponse returns the failure, and the generated adapter breaks the
// connection rather than end a truncated body as though it were complete.
func Stream(contentType string, r io.Reader) Response {
	return streamResponse{contentType: contentType, r: r}
}

type streamResponse struct {
	contentType string
	r           io.Reader
}

func (s streamResponse) write(w http.ResponseWriter, code int) error {
	if c, ok := s.r.(io.Closer); ok {
		// Deferred, so a reader that panics mid-copy is closed too. Its
		// error is dropped: by then the body is written or has already
		// failed, and closing the read side changes neither.
		defer func() { _ = c.Close() }()
	}
	ct := s.contentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(code)
	_, err := io.Copy(w, s.r)
	return err
}

// jsonValue's write lives here beside its siblings; the Json type and JSON
// constructor stay in json.go.
func (j jsonValue[T]) write(w http.ResponseWriter, code int) error {
	// Encoded in full before the status goes out. Encoding straight into
	// w committed the status first, so a payload that failed to encode —
	// a NaN, say — reached the client as its success status over an empty
	// body, and nothing could turn it into the error it was. The encoder
	// buffers the whole document internally either way; this only moves
	// the status line after it.
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(j.v); err != nil {
		return internalError(w, fmt.Errorf("servo: encoding the JSON response: %w", err))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, err := w.Write(body.Bytes())
	return err
}
