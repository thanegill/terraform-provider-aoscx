package aoscx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/aruba/aoscxgo"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// restStatusError is a request the switch answered with an unexpected status.
// answer is empty when the request carried a secret, since the switch may
// echo the request in its answer.
type restStatusError struct {
	method string
	path   string
	status int
	answer string
}

func (e *restStatusError) Error() string {
	if e.answer == "" {
		return fmt.Sprintf("%s %s answered HTTP %d", e.method, e.path, e.status)
	}
	return fmt.Sprintf("%s %s answered HTTP %d: %s", e.method, e.path, e.status, e.answer)
}

func isNotFound(err error) bool {
	statusErr, ok := err.(*restStatusError)
	return ok && statusErr.status == http.StatusNotFound
}

// restCall sends one request in the provider's REST session, the body as
// JSON, and returns the answer's status and body.
func restCall(sw *aoscxgo.Client, method string, path string, body interface{}) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "https://"+sw.Hostname+"/rest/"+sw.Version+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("accept", "*/*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-csrf-token", sw.Csrf)
	req.AddCookie(sw.Cookie)
	res, err := sw.Transport.RoundTrip(req)
	if err != nil {
		return 0, nil, fmt.Errorf("no response to %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s answered HTTP %d, then failed: %v", method, path, res.StatusCode, err)
	}
	return res.StatusCode, answer, nil
}

// restGet reads a resource into out.
func restGet(sw *aoscxgo.Client, path string, out interface{}) error {
	status, answer, err := restCall(sw, "GET", path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &restStatusError{"GET", path, status, shortAnswer(answer)}
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("GET %s answered what this cannot read: %v", path, err)
	}
	return nil
}

// restWrite sends a POST, PATCH or DELETE and checks the switch took it.
func restWrite(sw *aoscxgo.Client, method string, path string, body interface{}) error {
	return restWriteWithSecrets(sw, method, path, body, false)
}

// restWriteWithSecrets is restWrite, withholding the switch's answer to a
// refused request when the request carried a secret.
func restWriteWithSecrets(sw *aoscxgo.Client, method string, path string, body interface{}, secret bool) error {
	status, answer, err := restCall(sw, method, path, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusNoContent {
		shown := shortAnswer(answer)
		if secret {
			shown = ""
		}
		return &restStatusError{method, path, status, shown}
	}
	return nil
}

func shortAnswer(answer []byte) string {
	text := strings.TrimSpace(string(answer))
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

// restAttributes reads the named attributes of a resource. The switch
// answers each one asked for, null when it is unset.
func restAttributes(sw *aoscxgo.Client, path string, names ...string) (map[string]interface{}, error) {
	attributes := map[string]interface{}{}
	err := restGet(sw, path+"?attributes="+strings.Join(names, ","), &attributes)
	return attributes, err
}

// restMap is a map attribute as read, empty when it is unset.
func restMap(attributes map[string]interface{}, name string) (map[string]interface{}, error) {
	switch value := attributes[name].(type) {
	case nil:
		return map[string]interface{}{}, nil
	case map[string]interface{}:
		return value, nil
	default:
		return nil, fmt.Errorf("%s on the switch is not a map but %#v", name, value)
	}
}

// sameJSON is whether two values encode to the same JSON, as a value
// written and the same value read back do.
func sameJSON(a interface{}, b interface{}) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
}

// pathLocks serializes the writes to each path, from the read before a
// PATCH to the read after it: resources that share a map, such as the
// banners and LLDP in /system's other_config, are applied at once, and each
// would otherwise send back the map as it read it before the other's PATCH.
var pathLocks sync.Map

func lockPath(path string) func() {
	lock, _ := pathLocks.LoadOrStore(path, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	return lock.(*sync.Mutex).Unlock
}

// patchResource PATCHes attributes of a resource in one request: each of
// values as it is, and each of maps whole, the switch's map from a read just
// before it with these keys set, since a PATCH adds and changes the keys of a
// map but cannot remove them. It reads them back and fails unless each holds
// what it sent.
func patchResource(sw *aoscxgo.Client, path string, values map[string]interface{}, maps map[string]map[string]interface{}) error {
	defer lockPath(path)()
	body := map[string]interface{}{}
	for name, value := range values {
		body[name] = value
	}
	names := []string{}
	for name, keys := range maps {
		if len(keys) > 0 {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		current, err := restAttributes(sw, path, names...)
		if err != nil {
			return err
		}
		for _, name := range names {
			keys := maps[name]
			merged, err := restMap(current, name)
			if err != nil {
				return err
			}
			for key, value := range keys {
				merged[key] = value
			}
			body[name] = merged
		}
	}
	if len(body) == 0 {
		return nil
	}
	if err := restWrite(sw, "PATCH", path, body); err != nil {
		return err
	}
	written := make([]string, 0, len(body))
	for name := range body {
		written = append(written, name)
	}
	after, err := restAttributes(sw, path, written...)
	if err != nil {
		return err
	}
	for name, value := range body {
		if !sameJSON(after[name], value) {
			return fmt.Errorf("the switch took PATCH %s, but its %s is %v, not %v", path, name, after[name], value)
		}
	}
	return nil
}

// restKey is a collection member's key as its path takes it.
func restKey(key string) string {
	return url.PathEscape(key)
}

// importSingleton imports a resource the switch has exactly one of, by id.
func importSingleton(id string) *schema.ResourceImporter {
	return &schema.ResourceImporter{
		StateContext: func(ctx context.Context, d *schema.ResourceData, m interface{}) ([]*schema.ResourceData, error) {
			if d.Id() != id {
				return nil, fmt.Errorf("import this resource by the id %q, not %q", id, d.Id())
			}
			return []*schema.ResourceData{d}, nil
		},
	}
}

// forget drops a resource from state and leaves the switch as it is.
func forget(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	d.SetId("")
	return nil
}

// configInt is an integer from a map attribute as read, refusing another type.
func configInt(config map[string]interface{}, key string, unset int) (int, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return unset, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, fmt.Errorf("%s on the switch is %#v, not an integer", key, value)
	}
	return int(number), nil
}

// configBool is a boolean from a map attribute as read, refusing another type.
func configBool(config map[string]interface{}, key string, unset bool) (bool, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return unset, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s on the switch is %#v, not a boolean", key, value)
	}
	return flag, nil
}

// configString is a string from a map attribute as read, refusing another type.
func configString(config map[string]interface{}, key string, unset string) (string, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return unset, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s on the switch is %#v, not a string", key, value)
	}
	return text, nil
}
