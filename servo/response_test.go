package servo

import (
	"errors"
	"io"
	"math"
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

// A payload that does not encode must not go out as the success status over
// an empty body: the encoder runs before anything is written, and the
// failure becomes a whole 500 in the adapters' error envelope, with an error
// that carries the 500 so the caller knows only the log is left to write.
func TestWriteResponseEncodeFailureIsA500(t *testing.T) {
	cases := []struct {
		name string
		res  Response
		want string
	}{
		{"json NaN", JSON(map[string]float64{"v": math.NaN()}), "unsupported value"},
		{"json channel", JSON(make(chan int)), "unsupported type"},
		{"xml map", XML(map[string]int{"n": 1}), "unsupported type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			err := WriteResponse(w, 201, c.res)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want the encoder's error (%q)", err, c.want)
			}
			var hs HTTPStatus
			if !errors.As(err, &hs) || hs != Status.INTERNAL_SERVER_ERROR {
				t.Fatalf("err carries %+v, want Status.INTERNAL_SERVER_ERROR", hs)
			}
			if w.Code != 500 {
				t.Fatalf("code = %d, want 500 — the success status must not go out before the body encodes", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want the error envelope's", ct)
			}
			if got := w.Body.String(); got != "{\"error\":\"Internal Server Error\"}\n" {
				t.Fatalf("body = %q, want the canonical error envelope", got)
			}
		})
	}
}

// net/http panics on a code outside 100–999. A file generated before
// v3.5.0 passes a zero HTTPStatus's code straight through, so WriteResponse
// answers 500 for one rather than take the request down.
func TestWriteResponseRefusesACodeThatIsNoStatus(t *testing.T) {
	for _, code := range []int{0, 42, 1000} {
		w := httptest.NewRecorder()
		err := WriteResponse(w, code, JSON(1))
		var hs HTTPStatus
		if !errors.As(err, &hs) || hs.Code() != 500 {
			t.Fatalf("code %d: err = %v, want one carrying the 500", code, err)
		}
		if w.Code != 500 || !strings.Contains(w.Body.String(), "Internal Server Error") {
			t.Fatalf("code %d: wrote %d %q, want the 500 envelope", code, w.Code, w.Body.String())
		}
	}
}

type countingCloser struct {
	io.Reader
	closed int
}

func (c *countingCloser) Close() error { c.closed++; return nil }

type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.sent {
		return 0, errors.New("upstream reset")
	}
	f.sent = true
	return copy(p, "partial"), nil
}

// A Stream owns its reader: it is closed once the copy ends, however it
// ends — otherwise every file or upstream body a handler streams is a
// leaked descriptor or pooled connection.
func TestStreamClosesItsReader(t *testing.T) {
	t.Run("after a whole copy", func(t *testing.T) {
		r := &countingCloser{Reader: strings.NewReader("a,b\n")}
		w := httptest.NewRecorder()
		if err := WriteResponse(w, 200, Stream("text/csv", r)); err != nil {
			t.Fatalf("WriteResponse: %v", err)
		}
		if r.closed != 1 || w.Body.String() != "a,b\n" {
			t.Fatalf("closed %d times, body %q; want closed once after the body", r.closed, w.Body.String())
		}
	})
	t.Run("after a failed copy", func(t *testing.T) {
		r := &countingCloser{Reader: &failingReader{}}
		w := httptest.NewRecorder()
		err := WriteResponse(w, 200, Stream("", r))
		if err == nil || err.Error() != "upstream reset" {
			t.Fatalf("err = %v, want the reader's own error", err)
		}
		if r.closed != 1 {
			t.Fatalf("closed %d times, want once", r.closed)
		}
		// The status was committed before the reader failed, so this
		// error is not one WriteResponse could still answer — it carries
		// no status, which is how the adapter knows to break the
		// connection instead.
		var hs HTTPStatus
		if errors.As(err, &hs) {
			t.Fatalf("a mid-copy failure must not claim a status: %+v", hs)
		}
		if w.Code != 200 || w.Body.String() != "partial" {
			t.Fatalf("wrote %d %q", w.Code, w.Body.String())
		}
	})
}
