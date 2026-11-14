package aoscx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	restPrefix   = "/rest/v10.16"
	fakeUsername = "admin"
	fakePassword = "login p&ss=#"
	fakeCookie   = "session"
	fakeCsrf     = "csrf-token"
	// holdTimeout is how long a held read waits for a second one. With the
	// path lock it always waits this long; without it, a loaded builder must
	// still send the second read within it for the race to show.
	holdTimeout = 3 * time.Second
)

// fakeSwitch is a fake of the switch's REST API over HTTPS, as the provider
// talks to it, the Go side of tofu/switch/tests/mock-aoscx-rest.py in
// nixos-config. A login takes the credentials form-encoded in its body and
// refuses one with a query; every later request must send back its session
// cookie and CSRF token.
//
// resources holds resources by their path below /rest/v10.16, escaped as
// sent: `GET <path>?attributes=a,b` answers the attributes named, null for
// one it lacks, a GET without them or with ?selector=writable answers them
// all, and a PATCH sets each attribute in its body whole, a map included,
// so a client that sends part of a map loses the rest. members holds
// collections by path, each member by its key: a POST adds one keyed by the
// collection's index attribute in collectionKeys, and a member's own path
// takes GET and PATCH as a resource does, and DELETE. A reference written as
// its URI, such as a user's group, reads back as {name: URI}, as the switch
// answers it. Anything else answers 404.
//
// Every request is logged as one line: its method, path, query and JSON body,
// the attributes in the query sorted and the body's keys too, since the
// provider names them in map order.
type fakeSwitch struct {
	server *httptest.Server

	mu        sync.Mutex
	resources map[string]map[string]interface{}
	members   map[string]map[string]map[string]interface{}
	// refuse holds requests, as `METHOD path`, answered 400 with the
	// request's body echoed, as a switch may.
	refuse map[string]bool
	// drop holds requests, as `METHOD path`, whose connection is closed
	// without an answer. net/http sends a dropped GET again once when it
	// went over a reused connection, so the log may hold it twice.
	drop map[string]bool
	// afterWrite, when set, runs after each write the fake takes.
	afterWrite func(method string, path string)
	// holdRead, when set, is a `GET path?query` line whose first request is
	// answered only once a second one has been read, or after holdTimeout.
	holdRead     string
	heldRead     bool
	secondRead   chan struct{}
	requestLines []string
}

var collectionKeys = map[string]string{
	"/system/vlans":                         "id",
	"/system/syslog_remotes":                "remote_host",
	"/system/vrfs/default/ntp_associations": "address",
	"/system/snmpv3_users":                  "user_name",
	"/system/users":                         "name",
}

func newFakeSwitch(t *testing.T) *fakeSwitch {
	t.Helper()
	sw := &fakeSwitch{
		resources: map[string]map[string]interface{}{},
		members:   map[string]map[string]map[string]interface{}{},
		refuse:    map[string]bool{},
		drop:      map[string]bool{},
	}
	for collection := range collectionKeys {
		sw.members[collection] = map[string]map[string]interface{}{}
	}
	sw.server = httptest.NewTLSServer(http.HandlerFunc(sw.serve))
	t.Cleanup(sw.server.Close)
	return sw
}

func (sw *fakeSwitch) hostname() string {
	return sw.server.Listener.Addr().String()
}

// resource sets a resource's attributes from JSON.
func (sw *fakeSwitch) resource(path string, attributes string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.resources[path] = decodeObject(attributes)
}

// member sets a collection member's attributes from JSON.
func (sw *fakeSwitch) member(collection string, key string, attributes string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.members[collection][key] = decodeObject(attributes)
}

// attributes is a resource's or member's attributes as JSON with sorted keys,
// or "" when the fake has neither at path.
func (sw *fakeSwitch) attributes(path string) string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if attributes, ok := sw.resources[path]; ok {
		return canonicalJSON(attributes)
	}
	collection, key := splitMember(path)
	if attributes, ok := sw.members[collection][key]; ok {
		return canonicalJSON(attributes)
	}
	return ""
}

// requests returns the requests logged since the last call, and forgets them.
func (sw *fakeSwitch) requests() []string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	lines := sw.requestLines
	sw.requestLines = nil
	return lines
}

// writes returns the requests other than GETs logged since the last call to
// requests or writes, and forgets every request logged.
func (sw *fakeSwitch) writes() []string {
	lines := []string{}
	for _, line := range sw.requests() {
		if !strings.HasPrefix(line, "GET ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func decodeObject(text string) map[string]interface{} {
	object := map[string]interface{}{}
	if err := json.Unmarshal([]byte(text), &object); err != nil {
		panic(fmt.Sprintf("not a JSON object: %s: %v", text, err))
	}
	return object
}

func canonicalJSON(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// requestLine is a request as the log holds it.
func requestLine(method string, path string, query string, body string) string {
	line := method + " " + path
	if query != "" {
		values, err := url.ParseQuery(query)
		if err != nil {
			panic(err)
		}
		if names, ok := values["attributes"]; ok && len(names) == 1 {
			sorted := strings.Split(names[0], ",")
			sort.Strings(sorted)
			values["attributes"] = []string{strings.Join(sorted, ",")}
		}
		parts := []string{}
		for name, value := range values {
			parts = append(parts, name+"="+strings.Join(value, ","))
		}
		sort.Strings(parts)
		line += "?" + strings.Join(parts, "&")
	}
	if body != "" {
		var decoded interface{}
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			line += " " + body
		} else {
			line += " " + canonicalJSON(decoded)
		}
	}
	return line
}

func splitMember(path string) (string, string) {
	slash := strings.LastIndex(path, "/")
	key, err := url.PathUnescape(path[slash+1:])
	if err != nil {
		return "", ""
	}
	return path[:slash], key
}

// asRead is a written body as the switch reads it back: a reference as
// {name: URI}.
func asRead(body map[string]interface{}) map[string]interface{} {
	read := map[string]interface{}{}
	for name, value := range body {
		if text, ok := value.(string); ok && strings.HasPrefix(text, restPrefix+"/system/") {
			key, _ := url.PathUnescape(text[strings.LastIndex(text, "/")+1:])
			read[name] = map[string]interface{}{key: text}
			continue
		}
		read[name] = value
	}
	return read
}

func reply(w http.ResponseWriter, status int, text string) {
	w.WriteHeader(status)
	io.WriteString(w, text)
}

func (sw *fakeSwitch) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.EscapedPath(), restPrefix)
	sw.mu.Lock()
	line := requestLine(r.Method, path, r.URL.RawQuery, string(raw))
	sw.requestLines = append(sw.requestLines, line)
	request := r.Method + " " + path
	if !strings.HasPrefix(r.URL.EscapedPath(), restPrefix+"/") {
		sw.mu.Unlock()
		reply(w, http.StatusNotFound, "no such REST version")
		return
	}
	if sw.drop[request] {
		sw.mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	if sw.refuse[request] {
		sw.mu.Unlock()
		reply(w, http.StatusBadRequest, "refused: "+string(raw))
		return
	}
	if request == "POST /login" {
		sw.mu.Unlock()
		sw.login(w, r, string(raw))
		return
	}
	cookie, err := r.Cookie("id")
	if err != nil || cookie.Value != fakeCookie || r.Header.Get("x-csrf-token") != fakeCsrf {
		sw.mu.Unlock()
		reply(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var body map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			sw.mu.Unlock()
			reply(w, http.StatusBadRequest, "not a JSON object")
			return
		}
	}
	status, answer := sw.handle(r.Method, path, r.URL.Query(), body)
	if status < 300 && r.Method != "GET" && sw.afterWrite != nil {
		sw.afterWrite(r.Method, path)
	}
	hold := r.Method == "GET" && sw.holdRead != "" && line == sw.holdRead
	var wait chan struct{}
	if hold && !sw.heldRead {
		sw.heldRead = true
		sw.secondRead = make(chan struct{})
		wait = sw.secondRead
	} else if hold && sw.secondRead != nil {
		close(sw.secondRead)
		sw.secondRead = nil
	}
	sw.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-time.After(holdTimeout):
		}
	}
	reply(w, status, answer)
}

func (sw *fakeSwitch) login(w http.ResponseWriter, r *http.Request, raw string) {
	form, err := url.ParseQuery(raw)
	want := url.Values{"username": {fakeUsername}, "password": {fakePassword}}
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		err != nil || !reflect.DeepEqual(form, want) {
		reply(w, http.StatusUnauthorized, "Login failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "id", Value: fakeCookie, Path: "/"})
	if r.Header.Get("x-use-csrf-token") == "true" {
		w.Header().Set("X-Csrf-Token", fakeCsrf)
	}
	reply(w, http.StatusOK, "")
}

// handle answers a request with the fake's lock held.
func (sw *fakeSwitch) handle(method string, path string, query url.Values, body map[string]interface{}) (int, string) {
	if collection, ok := sw.members[path]; ok {
		if method != "POST" || body == nil {
			return http.StatusMethodNotAllowed, "not a method on a collection"
		}
		key := fmt.Sprint(body[collectionKeys[path]])
		if _, exists := collection[key]; exists {
			return http.StatusBadRequest, "already exists"
		}
		collection[key] = asRead(body)
		return http.StatusCreated, ""
	}
	if attributes, ok := sw.resources[path]; ok {
		if method == "DELETE" {
			return http.StatusMethodNotAllowed, "not a method on a resource"
		}
		return handleAttributes(attributes, method, query, body)
	}
	parent, key := splitMember(path)
	if collection, ok := sw.members[parent]; ok {
		attributes, exists := collection[key]
		switch {
		case !exists:
			return http.StatusNotFound, "Resource not found"
		case method == "DELETE":
			delete(collection, key)
			return http.StatusNoContent, ""
		default:
			return handleAttributes(attributes, method, query, body)
		}
	}
	return http.StatusNotFound, "Resource not found"
}

func handleAttributes(attributes map[string]interface{}, method string, query url.Values, body map[string]interface{}) (int, string) {
	switch {
	case method == "GET" && query.Get("attributes") != "":
		answer := map[string]interface{}{}
		for _, name := range strings.Split(query.Get("attributes"), ",") {
			answer[name] = attributes[name]
		}
		return http.StatusOK, canonicalJSON(answer)
	case method == "GET":
		return http.StatusOK, canonicalJSON(attributes)
	case method == "PATCH" && body != nil:
		for name, value := range asRead(body) {
			attributes[name] = value
		}
		return http.StatusNoContent, ""
	}
	return http.StatusMethodNotAllowed, "not a method the provider may use here"
}

// expectRequests fails unless got holds the request lines in want, in order.
func expectRequests(t *testing.T, got []string, want ...string) {
	t.Helper()
	normalized := make([]string, len(want))
	for i, line := range want {
		method, rest, _ := strings.Cut(line, " ")
		target, body, _ := strings.Cut(rest, " ")
		path, query, _ := strings.Cut(target, "?")
		normalized[i] = requestLine(method, path, query, body)
	}
	if len(normalized) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, normalized) {
		t.Errorf("the switch was sent\n  %s\nnot\n  %s", strings.Join(got, "\n  "), strings.Join(normalized, "\n  "))
	}
}

// expectAttributes fails unless the fake holds the attributes in want, as
// JSON, at path.
func expectAttributes(t *testing.T, sw *fakeSwitch, path string, want string) {
	t.Helper()
	got := sw.attributes(path)
	if want == "" {
		if got != "" {
			t.Errorf("the switch has %s: %s", path, got)
		}
		return
	}
	if wantCanonical := canonicalJSON(decodeObject(want)); got != wantCanonical {
		t.Errorf("the switch's %s is\n  %s\nnot\n  %s", path, got, wantCanonical)
	}
}
