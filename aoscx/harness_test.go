package aoscx

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/go-cty/cty/gocty"
	ctyjson "github.com/hashicorp/go-cty/cty/json"
	"github.com/hashicorp/go-cty/cty/msgpack"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// testProvider drives the provider through the SDK's gRPC server, in
// process, with the requests tofu sends it: validate, plan, apply, read and
// import. It checks no plan, state or error it answers holds any of secrets.
type testProvider struct {
	t        *testing.T
	provider *schema.Provider
	server   *schema.GRPCProviderServer

	mu      sync.Mutex
	secrets []string
}

func newTestProvider(t *testing.T) *testProvider {
	provider := Provider()
	return &testProvider{
		t:        t,
		provider: provider,
		server:   schema.NewGRPCProviderServer(provider),
		secrets:  []string{fakePassword},
	}
}

// configuredProvider is a provider logged in to sw, whose log it then clears.
func configuredProvider(t *testing.T, sw *fakeSwitch) *testProvider {
	t.Helper()
	p := newTestProvider(t)
	if errs := p.configure(sw.hostname(), fakeUsername, fakePassword); errs != "" {
		t.Fatalf("configure: %s", errs)
	}
	sw.requests()
	return p
}

// secret makes every later check fail on a plan, state or error holding text.
func (p *testProvider) secret(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secrets = append(p.secrets, text)
}

func (p *testProvider) checkNoSecret(what string, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, secret := range p.secrets {
		if strings.Contains(text, secret) {
			p.t.Errorf("%s holds the secret %q: %s", what, secret, text)
		}
	}
}

func (p *testProvider) configure(hostname string, username string, password string) string {
	ty := schema.InternalMap(p.provider.Schema).CoreConfigSchema().ImpliedType()
	config := cty.ObjectVal(map[string]cty.Value{
		"hostname": cty.StringVal(hostname),
		"username": cty.StringVal(username),
		"password": cty.StringVal(password),
	})
	resp, err := p.server.ConfigureProvider(context.Background(), &tfprotov5.ConfigureProviderRequest{
		TerraformVersion: "1.11.0",
		Config:           encode(config, ty),
	})
	return p.errors("configure", resp.Diagnostics, err)
}

func (p *testProvider) resourceType(typeName string) cty.Type {
	return p.provider.ResourcesMap[typeName].CoreConfigSchema().ImpliedType()
}

// config is a resource's configuration: each attribute in attributes, as Go
// values gocty converts, and null for each other one.
func (p *testProvider) config(typeName string, attributes map[string]interface{}) cty.Value {
	ty := p.resourceType(typeName)
	values := map[string]cty.Value{}
	for name, attributeType := range ty.AttributeTypes() {
		value, set := attributes[name]
		if !set {
			values[name] = cty.NullVal(attributeType)
			continue
		}
		converted, err := gocty.ToCtyValue(value, attributeType)
		if err != nil {
			panic(fmt.Sprintf("%s.%s: %v", typeName, name, err))
		}
		values[name] = converted
	}
	for name := range attributes {
		if !ty.HasAttribute(name) {
			panic(fmt.Sprintf("%s has no attribute %s", typeName, name))
		}
	}
	return cty.ObjectVal(values)
}

func encode(value cty.Value, ty cty.Type) *tfprotov5.DynamicValue {
	data, err := msgpack.Marshal(value, ty)
	if err != nil {
		panic(err)
	}
	return &tfprotov5.DynamicValue{MsgPack: data}
}

func (p *testProvider) decode(what string, dynamic *tfprotov5.DynamicValue, ty cty.Type) cty.Value {
	if dynamic == nil {
		return cty.NullVal(ty)
	}
	value, err := msgpack.Unmarshal(dynamic.MsgPack, ty)
	if err != nil {
		panic(err)
	}
	p.checkNoSecret(what, value.GoString())
	return value
}

// errors joins the error diagnostics, empty when there are none.
func (p *testProvider) errors(what string, diags []*tfprotov5.Diagnostic, err error) string {
	if err != nil {
		p.checkNoSecret(what+" error", err.Error())
		return err.Error()
	}
	errs := []string{}
	for _, d := range diags {
		p.checkNoSecret(what+" diagnostic", d.Summary+" "+d.Detail)
		if d.Severity == tfprotov5.DiagnosticSeverityError {
			errs = append(errs, strings.TrimSpace(d.Summary+" "+d.Detail))
		}
	}
	return strings.Join(errs, "; ")
}

func (p *testProvider) validate(typeName string, config cty.Value) string {
	resp, err := p.server.ValidateResourceTypeConfig(context.Background(), &tfprotov5.ValidateResourceTypeConfigRequest{
		TypeName:           typeName,
		Config:             encode(config, p.resourceType(typeName)),
		ClientCapabilities: &tfprotov5.ValidateResourceTypeConfigClientCapabilities{WriteOnlyAttributesAllowed: true},
	})
	return p.errors("validate", resp.Diagnostics, err)
}

// proposed is the new state tofu proposes from the configuration: each
// computed attribute left unset kept as it is in prior.
func (p *testProvider) proposed(typeName string, prior cty.Value, config cty.Value) cty.Value {
	resource := p.provider.ResourcesMap[typeName]
	values := config.AsValueMap()
	for name, value := range values {
		attribute := resource.Schema[name]
		computed := name == "id" || (attribute != nil && attribute.Computed)
		if computed && value.IsNull() && !prior.IsNull() {
			values[name] = prior.GetAttr(name)
		}
	}
	return cty.ObjectVal(values)
}

// plannedChange is a plan of one resource.
type plannedChange struct {
	planned cty.Value
	private []byte
	replace bool
}

// changes is whether the plan changes prior.
func (c plannedChange) changes(prior cty.Value) bool {
	return !c.planned.RawEquals(prior)
}

// plan validates config and plans it against prior, null to create.
func (p *testProvider) plan(typeName string, prior cty.Value, config cty.Value) (plannedChange, string) {
	if errs := p.validate(typeName, config); errs != "" {
		return plannedChange{}, errs
	}
	ty := p.resourceType(typeName)
	resp, err := p.server.PlanResourceChange(context.Background(), &tfprotov5.PlanResourceChangeRequest{
		TypeName:         typeName,
		PriorState:       encode(prior, ty),
		ProposedNewState: encode(p.proposed(typeName, prior, config), ty),
		Config:           encode(config, ty),
	})
	if errs := p.errors("plan", resp.Diagnostics, err); errs != "" {
		return plannedChange{}, errs
	}
	planned := p.decode("planned state", resp.PlannedState, ty)
	return plannedChange{planned, resp.PlannedPrivate, !prior.IsNull() && len(resp.RequiresReplace) > 0}, ""
}

func (p *testProvider) applyChange(typeName string, prior cty.Value, change plannedChange, config cty.Value) (cty.Value, string) {
	ty := p.resourceType(typeName)
	resp, err := p.server.ApplyResourceChange(context.Background(), &tfprotov5.ApplyResourceChangeRequest{
		TypeName:       typeName,
		PriorState:     encode(prior, ty),
		PlannedState:   encode(change.planned, ty),
		Config:         encode(config, ty),
		PlannedPrivate: change.private,
	})
	errs := p.errors("apply", resp.Diagnostics, err)
	return p.decode("new state", resp.NewState, ty), errs
}

// apply plans config against prior, null to create, and applies the plan
// as tofu does: nothing when it changes nothing, and a replacement as a
// create planned against no prior state, which plans or fails before the
// destroy, then the destroy and that create.
func (p *testProvider) apply(typeName string, prior cty.Value, config cty.Value) (cty.Value, string) {
	change, errs := p.plan(typeName, prior, config)
	if errs != "" {
		return prior, errs
	}
	if !change.changes(prior) {
		return prior, ""
	}
	if change.replace {
		if change, errs = p.plan(typeName, p.none(typeName), config); errs != "" {
			return prior, errs
		}
		if errs := p.destroy(typeName, prior); errs != "" {
			return prior, errs
		}
		prior = p.none(typeName)
	}
	return p.applyChange(typeName, prior, change, config)
}

// none is the state of a resource that does not exist yet.
func (p *testProvider) none(typeName string) cty.Value {
	return cty.NullVal(p.resourceType(typeName))
}

func (p *testProvider) create(typeName string, config cty.Value) (cty.Value, string) {
	return p.apply(typeName, p.none(typeName), config)
}

func (p *testProvider) destroy(typeName string, prior cty.Value) string {
	ty := p.resourceType(typeName)
	_, errs := p.applyChange(typeName, prior, plannedChange{planned: cty.NullVal(ty)}, cty.NullVal(ty))
	return errs
}

// read refreshes state, null when the provider forgot the resource.
func (p *testProvider) read(typeName string, state cty.Value) (cty.Value, string) {
	ty := p.resourceType(typeName)
	resp, err := p.server.ReadResource(context.Background(), &tfprotov5.ReadResourceRequest{
		TypeName:     typeName,
		CurrentState: encode(state, ty),
	})
	errs := p.errors("read", resp.Diagnostics, err)
	return p.decode("read state", resp.NewState, ty), errs
}

// importState imports the resource with id and reads it, as `tofu import`
// does.
func (p *testProvider) importState(typeName string, id string) (cty.Value, string) {
	ty := p.resourceType(typeName)
	resp, err := p.server.ImportResourceState(context.Background(), &tfprotov5.ImportResourceStateRequest{
		TypeName: typeName,
		ID:       id,
	})
	if errs := p.errors("import", resp.Diagnostics, err); errs != "" {
		return cty.NullVal(ty), errs
	}
	if len(resp.ImportedResources) != 1 {
		return cty.NullVal(ty), fmt.Sprintf("imported %d resources, not one", len(resp.ImportedResources))
	}
	imported := p.decode("imported state", resp.ImportedResources[0].State, ty)
	return p.read(typeName, imported)
}

// attributes is a state as JSON values: numbers as float64, lists and sets
// as []interface{}.
func attributes(state cty.Value) map[string]interface{} {
	if state.IsNull() {
		return nil
	}
	encoded, err := ctyjson.Marshal(state, state.Type())
	if err != nil {
		panic(err)
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

// expectState fails unless state holds each attribute in want, as JSON.
func expectState(t *testing.T, state cty.Value, want string) {
	t.Helper()
	got := attributes(state)
	for name, value := range decodeObject(want) {
		if !reflect.DeepEqual(got[name], value) {
			t.Errorf("state has %s = %s, not %s", name, canonicalJSON(got[name]), canonicalJSON(value))
		}
	}
}

func expectOK(t *testing.T, what string, errs string) {
	t.Helper()
	if errs != "" {
		t.Fatalf("%s: %s", what, errs)
	}
}

func expectError(t *testing.T, what string, errs string, want string) {
	t.Helper()
	if errs == "" {
		t.Fatalf("%s succeeded, not failing with %q", what, want)
	}
	if !strings.Contains(errs, want) {
		t.Errorf("%s failed with %q, not %q", what, errs, want)
	}
}

// expectNoChanges fails unless config planned against state changes nothing.
func (p *testProvider) expectNoChanges(t *testing.T, typeName string, state cty.Value, config cty.Value) {
	t.Helper()
	change, errs := p.plan(typeName, state, config)
	expectOK(t, "plan", errs)
	if change.changes(state) {
		t.Errorf("plan changes\n  %#v\nto\n  %#v", attributes(state), attributes(change.planned))
	}
}
