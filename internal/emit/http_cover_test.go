package emit

import (
	"strings"
	"testing"
)

// shopAlphaSrc and shopBetaSrc each declare a handler called List, both
// on the default group, so their two adapters cannot both be handleList.
const shopAlphaSrc = `
package alpha

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Item struct{ Name string }

//servo:get /alpha/items
func List(ctx context.Context) (servo.Json[[]Item], error) {
	return servo.JSON([]Item{{Name: "alpha"}}), nil
}
`

const shopBetaSrc = `
package beta

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Item struct{ Name string }

//servo:get /beta/items
func List(ctx context.Context) (servo.Json[[]Item], error) {
	return servo.JSON([]Item{{Name: "beta"}}), nil
}
`

// shopSrc is an injector package that serves routes of its own: a form
// route binding a field of every scalar kind the scanner allows, from a
// path segment, headers and the form; a handler borrowing a scope's
// accessor; and middleware limited to the default group by its name.
const shopSrc = `
package shop

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

func NewHTTPConfig() *servo.HTTPConfig { return &servo.HTTPConfig{} }

type Profile struct {
	ID     uint64  ` + "`path:\"id\"`" + `
	Admin  bool    ` + "`form:\"admin\"`" + `
	Age    int8    ` + "`form:\"age\"`" + `
	Rank   int16   ` + "`form:\"rank\"`" + `
	Score  int32   ` + "`form:\"score\"`" + `
	Total  int64   ` + "`form:\"total\"`" + `
	Count  uint    ` + "`form:\"count\"`" + `
	Small  uint8   ` + "`form:\"small\"`" + `
	Medium uint16  ` + "`form:\"medium\"`" + `
	Large  uint32  ` + "`form:\"large\"`" + `
	Ratio  float32 ` + "`form:\"ratio\"`" + `
	Weight float64 ` + "`form:\"weight\"`" + `
	Name   string  ` + "`form:\"name\"`" + `
	Retry  int     ` + "`header:\"X-Retry\"`" + `
	Trace  string  ` + "`header:\"X-Trace\"`" + `
}

//servo:post /profiles/{id}
func SaveProfile(ctx context.Context, req *Profile) (servo.Json[*Profile], error) {
	return servo.JSON(req), nil
}

type RoomKey string

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "lobby", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

//servo:get /room
func CurrentRoom(ctx context.Context, rooms Rooms) (servo.Json[string], error) {
	_, release, err := rooms.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return servo.JSON("lobby"), nil
}

type Gate struct{}

func NewGate() *Gate { return &Gate{} }

func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Gate", "default")
		next.ServeHTTP(w, r)
	})
}
`

// shopPkgs is the shop fixture's module, the injector last.
func shopPkgs() []fixturePkg {
	return []fixturePkg{
		{"example.com/alpha", shopAlphaSrc},
		{"example.com/beta", shopBetaSrc},
		{"example.com/shop", shopSrc},
	}
}

// emitShop generates the shop fixture's production file.
func emitShop(t *testing.T) string {
	t.Helper()
	resolved, spec := buildFixture(t, fixtureSpec{
		scopes: [][2]string{{"Room", "Rooms"}},
		http:   &fixtureHTTP{uses: []fixtureUse{{typ: "Gate", groups: []string{"default"}}}},
	}, shopPkgs()...)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return string(out)
}

// adapterBody is one generated adapter, from its signature to its closing
// brace.
func adapterBody(t *testing.T, src, adapter string) string {
	t.Helper()
	start := strings.Index(src, "func (s *httpServer) "+adapter+"(")
	if start < 0 {
		t.Fatalf("no %s adapter:\n%s", adapter, src)
	}
	return src[start : start+strings.Index(src[start:], "\n}\n")]
}

// TestEmitHTTPFormRouteParsesEveryScalarKind pins the decode of a form
// route: the body is capped and parsed as a form, multipart or not, and
// every bound field gets the strconv call of its own width, so an int8
// form value of 200 is refused rather than wrapped around. Each refusal
// names where the value came from.
func TestEmitHTTPFormRouteParsesEveryScalarKind(t *testing.T) {
	src := emitShop(t)
	body := adapterBody(t, src, "handleSaveProfile")
	for _, want := range []string{
		"\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n\t// ParseMultipartForm falls through to ParseForm on urlencoded\n",
		"if err := r.ParseMultipartForm(s.maxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {\n\t\tservoimpl.BodyError(w, err, \"malformed form body\")",
		`if raw := r.PathValue("id"); raw != "" {`,
		`servoimpl.WriteError(w, http.StatusBadRequest, "path segment \"id\" is not a valid uint64")`,
		`if raw := r.Header.Get("X-Retry"); raw != "" {`,
		`servoimpl.WriteError(w, http.StatusBadRequest, "header \"X-Retry\" is not a valid int")`,
		`req.Trace = r.Header.Get("X-Trace")`,
		`if raw := r.PostForm.Get("age"); raw != "" {`,
		`servoimpl.WriteError(w, http.StatusBadRequest, "form value \"age\" is not a valid int8")`,
		`req.Name = r.PostForm.Get("name")`,
		// The handler lives in the injector's own package, which the
		// generated file cannot import: it is called bare.
		"res, err := SaveProfile(r.Context(), req)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleSaveProfile missing %q\n---\n%s", want, body)
		}
	}
	for _, kind := range []struct{ parse, convert string }{
		{"strconv.ParseBool(raw)", "req.Admin = v"},
		{"strconv.ParseInt(raw, 10, 8)", "req.Age = int8(v)"},
		{"strconv.ParseInt(raw, 10, 16)", "req.Rank = int16(v)"},
		{"strconv.ParseInt(raw, 10, 32)", "req.Score = int32(v)"},
		{"strconv.ParseInt(raw, 10, 64)", "req.Total = v"},
		{"strconv.ParseUint(raw, 10, 0)", "req.Count = uint(v)"},
		{"strconv.ParseUint(raw, 10, 8)", "req.Small = uint8(v)"},
		{"strconv.ParseUint(raw, 10, 16)", "req.Medium = uint16(v)"},
		{"strconv.ParseUint(raw, 10, 32)", "req.Large = uint32(v)"},
		{"strconv.ParseUint(raw, 10, 64)", "req.ID = v"},
		{"strconv.ParseFloat(raw, 32)", "req.Ratio = float32(v)"},
		{"strconv.ParseFloat(raw, 64)", "req.Weight = v"},
		{"strconv.ParseInt(raw, 10, 0)", "req.Retry = int(v)"},
	} {
		if !strings.Contains(body, "v, err := "+kind.parse+"\n") || !strings.Contains(body, "\t\t"+kind.convert+"\n") {
			t.Errorf("handleSaveProfile does not parse with %s into %s\n---\n%s", kind.parse, kind.convert, body)
		}
	}
	// A form route qualifies errors.Is, which nothing else here imports.
	assertImportsBalanced(t, src)
	typecheckGenerated(t, []byte(src), shopPkgs()...)
}

// TestEmitHTTPSameNamedHandlersGetPackageQualifiedAdapters: two handlers
// called List, from two packages, share one group's server, and an
// adapter is a method on it. Each adapter is named for its package, so
// neither is handleList2, which would not say which List it serves.
func TestEmitHTTPSameNamedHandlersGetPackageQualifiedAdapters(t *testing.T) {
	src := emitShop(t)
	for _, want := range []string{
		`mux.HandleFunc("GET /alpha/items", s.handleAlphaList)`,
		`mux.HandleFunc("GET /beta/items", s.handleBetaList)`,
		"res, err := alpha.List(r.Context())",
		"res, err := beta.List(r.Context())",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
	if strings.Contains(src, "handleList") {
		t.Errorf("an adapter is still named handleList")
	}
	if t.Failed() {
		t.Logf("--- generated ---\n%s", src)
	}
}

// TestEmitHTTPHandlerBorrowsAScopeAccessor: an accessor is not built by a
// provider, so it has no local in New to read; the adapter reaches it on
// the App, in the field scopeSetup fills before any server exists.
func TestEmitHTTPHandlerBorrowsAScopeAccessor(t *testing.T) {
	src := emitShop(t)
	body := adapterBody(t, src, "handleCurrentRoom")
	if !strings.Contains(body, "res, err := CurrentRoom(r.Context(), s.app.rooms)") {
		t.Errorf("handleCurrentRoom does not hand the handler the accessor field:\n%s", body)
	}
	if !strings.Contains(src, "a.rooms = roomsAccessor{s: a.roomKeyScope}") {
		t.Errorf("the accessor field the adapter reads is never set:\n%s", src)
	}
}

// TestEmitHTTPUseOnTheDefaultGroupByName: servo.Groups("default") names
// the group every injector serves, which the server is emitted under the
// empty name. The middleware has to land on that server, not on a group
// called "default" that nothing serves.
func TestEmitHTTPUseOnTheDefaultGroupByName(t *testing.T) {
	src := emitShop(t)
	ctor := src[strings.Index(src, "func newHttpServer("):]
	ctor = ctor[:strings.Index(ctor, "\n}\n")]
	if !strings.Contains(ctor, "handler = a.gate.Middleware(handler)") {
		t.Errorf("the default group's server is not wrapped in the middleware:\n%s", ctor)
	}
}

// TestEmitHTTPShopRuns drives the shop's generated server in a module of
// its own, through App.HTTPHandler: the form binds every kind, each value
// one past its type's range is a 400 naming its source, the same-named
// handlers each answer their own route, and the default group's
// middleware runs.
func TestEmitHTTPShopRuns(t *testing.T) {
	src := emitShop(t)
	runGenerated(t, "shop", []byte(src), map[string]string{
		"alpha/alpha.go": shopAlphaSrc,
		"beta/beta.go":   shopBetaSrc,
		"shop/shop.go":   shopSrc,
		"shop/shop_test.go": `package shop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func serve(t *testing.T, method, target string, form url.Values, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	app.HTTPHandler("").ServeHTTP(rec, req)
	return rec
}

func TestFormBindsEveryKind(t *testing.T) {
	form := url.Values{
		"admin": {"true"}, "age": {"-128"}, "rank": {"-32768"}, "score": {"-2147483648"},
		"total": {"-9223372036854775808"}, "count": {"7"}, "small": {"255"}, "medium": {"65535"},
		"large": {"4294967295"}, "ratio": {"0.5"}, "weight": {"2.25"}, "name": {"Ada"},
	}
	rec := serve(t, "POST", "/profiles/18446744073709551615", form, map[string]string{"X-Retry": "3", "X-Trace": "abc"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := Profile{
		ID: 18446744073709551615, Admin: true, Age: -128, Rank: -32768, Score: -2147483648,
		Total: -9223372036854775808, Count: 7, Small: 255, Medium: 65535, Large: 4294967295,
		Ratio: 0.5, Weight: 2.25, Name: "Ada", Retry: 3, Trace: "abc",
	}
	if got != want {
		t.Errorf("bound %+v, want %+v", got, want)
	}
	if rec.Header().Get("X-Gate") != "default" {
		t.Errorf("the default group's middleware did not run")
	}
}

func TestOutOfRangeIsABadRequest(t *testing.T) {
	cases := []struct {
		target string
		form   url.Values
		header map[string]string
		want   string
	}{
		{"/profiles/-1", nil, nil, "path segment \"id\" is not a valid uint64"},
		{"/profiles/1", url.Values{"admin": {"maybe"}}, nil, "form value \"admin\" is not a valid bool"},
		{"/profiles/1", url.Values{"age": {"128"}}, nil, "form value \"age\" is not a valid int8"},
		{"/profiles/1", url.Values{"rank": {"32768"}}, nil, "form value \"rank\" is not a valid int16"},
		{"/profiles/1", url.Values{"score": {"2147483648"}}, nil, "form value \"score\" is not a valid int32"},
		{"/profiles/1", url.Values{"total": {"9223372036854775808"}}, nil, "form value \"total\" is not a valid int64"},
		{"/profiles/1", url.Values{"count": {"-1"}}, nil, "form value \"count\" is not a valid uint"},
		{"/profiles/1", url.Values{"small": {"256"}}, nil, "form value \"small\" is not a valid uint8"},
		{"/profiles/1", url.Values{"medium": {"65536"}}, nil, "form value \"medium\" is not a valid uint16"},
		{"/profiles/1", url.Values{"large": {"4294967296"}}, nil, "form value \"large\" is not a valid uint32"},
		{"/profiles/1", url.Values{"ratio": {"1e39"}}, nil, "form value \"ratio\" is not a valid float32"},
		{"/profiles/1", url.Values{"weight": {"heavy"}}, nil, "form value \"weight\" is not a valid float64"},
		{"/profiles/1", nil, map[string]string{"X-Retry": "soon"}, "header \"X-Retry\" is not a valid int"},
	}
	for _, c := range cases {
		rec := serve(t, "POST", c.target, c.form, c.header)
		var body struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %v: %v: %s", c.target, c.form, err, rec.Body)
		}
		if rec.Code != http.StatusBadRequest || body.Error != c.want {
			t.Errorf("%s %v %v: %d %q, want 400 %q", c.target, c.form, c.header, rec.Code, body.Error, c.want)
		}
	}
}

func TestSameNamedHandlersServeTheirOwnRoutes(t *testing.T) {
	for _, pkg := range []string{"alpha", "beta"} {
		rec := serve(t, "GET", "/"+pkg+"/items", nil, nil)
		if want := "[{\"Name\":\"" + pkg + "\"}]\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET /%s/items: %d %q, want 200 %q", pkg, rec.Code, rec.Body, want)
		}
	}
}
`,
	})
}

// TestExportedIdentNamesEveryGroup: a group's server type is named after
// the group, and the group grammar allows names made only of separators.
// Those still need an identifier, and get a fixed one rather than none —
// "http" + "" + "Server" would be the default group's own type.
func TestExportedIdentNamesEveryGroup(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"telemetry", "Telemetry"},
		{"back-office", "BackOffice"},
		{"admin_api", "AdminApi"},
		{"v2", "V2"},
		{"-", "Group"},
		{"__", "Group"},
	} {
		if got := exportedIdent(c.name); got != c.want {
			t.Errorf("exportedIdent(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
