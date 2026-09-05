package servo

import (
	"encoding/json"
	"encoding/xml"
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
	// write sets the content type, writes code, then the body.
	write(w http.ResponseWriter, code int) error
}

// WriteResponse writes res with the given status code. It exists because
// the generated adapters live in the user's package, where the sealed write
// method is unreachable. A nil res writes the code alone.
func WriteResponse(w http.ResponseWriter, code int, res Response) error {
	if res == nil {
		w.WriteHeader(code)
		return nil
	}
	return res.write(w, code)
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
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	return xml.NewEncoder(w).Encode(x.v)
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

// Stream copies r as the response body — files, pipes, anything io.Copy
// can drain. Empty contentType means application/octet-stream.
func Stream(contentType string, r io.Reader) Response {
	return streamResponse{contentType: contentType, r: r}
}

type streamResponse struct {
	contentType string
	r           io.Reader
}

func (s streamResponse) write(w http.ResponseWriter, code int) error {
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	return json.NewEncoder(w).Encode(j.v)
}
