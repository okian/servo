package servo

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type xmlPayload struct {
	Name string `xml:"name"`
}

// Every response kind writes its own content type, the given status code,
// and its body — through WriteResponse, which is exactly the call the
// emitted adapter makes.
func TestWriteResponseKinds(t *testing.T) {
	cases := []struct {
		name        string
		res         Response
		code        int
		contentType string
		body        string
		header      map[string]string
	}{
		{
			name: "json", res: JSON(map[string]int{"n": 1}), code: 200,
			contentType: "application/json", body: "{\"n\":1}\n",
		},
		{
			name: "xml", res: XML(xmlPayload{Name: "espresso"}), code: 201,
			contentType: "application/xml; charset=utf-8", body: "<xmlPayload><name>espresso</name></xmlPayload>",
		},
		{
			name: "text", res: Text("pong"), code: 200,
			contentType: "text/plain; charset=utf-8", body: "pong",
		},
		{
			name: "html", res: HTML("<h1>hi</h1>"), code: 200,
			contentType: "text/html; charset=utf-8", body: "<h1>hi</h1>",
		},
		{
			name: "blob", res: Blob("application/pdf", []byte{0x25, 0x50}), code: 200,
			contentType: "application/pdf", body: "%P",
		},
		{
			name: "blob default type", res: Blob("", []byte("x")), code: 200,
			contentType: "application/octet-stream", body: "x",
		},
		{
			name: "redirect", res: Redirect("/new/place"), code: 302,
			contentType: "", body: "", header: map[string]string{"Location": "/new/place"},
		},
		{
			name: "stream", res: Stream("text/csv", strings.NewReader("a,b\n")), code: 200,
			contentType: "text/csv", body: "a,b\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := WriteResponse(w, c.code, c.res); err != nil {
				t.Fatalf("WriteResponse: %v", err)
			}
			if w.Code != c.code {
				t.Errorf("code = %d, want %d", w.Code, c.code)
			}
			if got := w.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("Content-Type = %q, want %q", got, c.contentType)
			}
			if got := w.Body.String(); got != c.body {
				t.Errorf("body = %q, want %q", got, c.body)
			}
			for k, v := range c.header {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

// A nil Response writes the code and nothing else — the shape behind
// `return nil, servo.Status.NO_CONTENT`.
func TestWriteResponseNil(t *testing.T) {
	w := httptest.NewRecorder()
	if err := WriteResponse(w, 204, nil); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatalf("code=%d body=%q ct=%q, want a bare 204", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
}

// The typed forms still expose their payload, and both are assignable to
// the base interface — which is what lets the emitted helper take every
// kind through one servo.Response parameter.
func TestTypedResponsesAreResponses(t *testing.T) {
	var res Response = JSON(&orderResp{ID: "o-1"})
	if res == nil {
		t.Fatalf("Json must satisfy Response")
	}
	x := XML(xmlPayload{Name: "n"})
	if got := x.Value().Name; got != "n" {
		t.Fatalf("Xml.Value() = %q", got)
	}
	res = x
	if res == nil {
		t.Fatalf("Xml must satisfy Response")
	}
}

// An encoding failure surfaces as WriteResponse's error, not a panic —
// json can't marshal a channel.
func TestWriteResponseEncodeError(t *testing.T) {
	w := httptest.NewRecorder()
	if err := WriteResponse(w, 200, JSON(make(chan int))); err == nil {
		t.Fatalf("expected an encoding error")
	}
}

var _ io.Reader = strings.NewReader("") // keep io imported for the Stream case above
