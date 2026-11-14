package aoscx

import (
	"testing"
)

// portOnSwitch is port 1/1/1 with a description, an MTU and VLANs 1 and 10,
// as a switch has them before any apply, in the shape firmware PL.10.16
// answers: the top-level admin null, and user_config without admin for a
// port that is up.
func portOnSwitch(sw *fakeSwitch) {
	sw.resource(port1, `{"description": "old", "admin": null, "admin_state": "up", "user_config": {"mtu": 9000},
		"vlan_mode": "access", "vlan_tag": {"1": "/rest/v10.16/system/vlans/1"}, "vlan_trunks": {}}`)
	sw.member("/system/vlans", "1", `{"id": 1, "name": "DEFAULT_VLAN_1", "admin": "up"}`)
	sw.member("/system/vlans", "10", `{"id": 10, "name": "ten", "admin": "up"}`)
	sw.member("/system/vlans", "20", `{"id": 20, "name": "twenty", "admin": "up"}`)
}

// A port always exists, so create adopts it, with the declared description.
func TestInterfaceAdoptsAPortWithItsDeclaredDescription(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_interface", map[string]interface{}{"name": "1/1/1", "description": "uplink"})
	state, errs := p.create("aoscx_interface", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET "+port1,
		"PATCH "+port1+` {"description": "uplink"}`,
		"GET "+port1,
	)
	expectState(t, state, `{"id": "1/1/1", "name": "1/1/1", "description": "uplink", "admin_state": "up"}`)
	p.expectNoChanges(t, "aoscx_interface", state, config)
	expectAttributes(t, sw, port1, `{"description": "uplink", "admin": null, "admin_state": "up", "user_config": {"mtu": 9000},
		"vlan_mode": "access", "vlan_tag": {"1": "/rest/v10.16/system/vlans/1"}, "vlan_trunks": {}}`)

	config = p.config("aoscx_interface", map[string]interface{}{"name": "1/1/1", "description": "to the router"})
	state, errs = p.apply("aoscx_interface", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(), "PATCH "+port1+` {"description": "to the router"}`)
	p.expectNoChanges(t, "aoscx_interface", state, config)
}

// With the top-level admin null, the admin state is user_config's, up when
// that has none.
func TestInterfaceReadsTheAdminStateFromUserConfig(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)
	config := p.config("aoscx_interface", map[string]interface{}{"name": "1/1/1", "description": "old", "admin_state": "down"})
	state, errs := p.create("aoscx_interface", config)
	expectOK(t, "create", errs)
	expectState(t, state, `{"admin_state": "up"}`)

	sw.resource(port1, `{"description": "old", "admin": null, "admin_state": "down", "user_config": {"admin": "down", "mtu": 9000}}`)
	state, errs = p.read("aoscx_interface", state)
	expectOK(t, "read", errs)
	expectState(t, state, `{"admin_state": "down"}`)
	p.expectNoChanges(t, "aoscx_interface", state, config)
}

// A failed read of a port is an error: the resource is kept, and an update
// sends nothing.
func TestInterfaceFailsWhenTheSwitchDoesNotAnswer(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)
	state, errs := p.create("aoscx_interface", p.config("aoscx_interface", map[string]interface{}{"name": "1/1/1", "description": "uplink"}))
	expectOK(t, "create", errs)
	sw.requests()

	sw.drop["GET "+port1] = true
	_, errs = p.read("aoscx_interface", state)
	expectError(t, "read", errs, "Error in reading 1/1/1: no response to GET")

	_, errs = p.apply("aoscx_interface", state, p.config("aoscx_interface", map[string]interface{}{"name": "1/1/1", "description": "new"}))
	expectError(t, "update", errs, "Error in reading 1/1/1")
	expectRequests(t, sw.writes())

	delete(sw.drop, "GET "+port1)
	sw.refuse["GET "+port1] = true
	_, errs = p.read("aoscx_interface", state)
	expectError(t, "read", errs, "Error in reading 1/1/1: 400 Bad Request")
}

// A collection member is forgotten only when the switch answers 404 for it;
// a read it refuses or does not answer is an error.
func TestMemberReadFailuresAreErrors(t *testing.T) {
	for _, c := range []struct {
		typeName, collection, key, attributes string
	}{
		{"aoscx_syslog_remote", "/system/syslog_remotes", "10.0.0.9", `{"remote_host": "10.0.0.9"}`},
		{"aoscx_ntp_server", "/system/vrfs/default/ntp_associations", "ntp.example.com", `{"address": "ntp.example.com"}`},
		{"aoscx_snmpv3_user", "/system/snmpv3_users", "monitor", `{"user_name": "monitor"}`},
		{"aoscx_user", "/system/users", "ops", `{"name": "ops", "user_group": {"administrators": "/rest/v10.16/system/user_groups/administrators"}}`},
	} {
		t.Run(c.typeName, func(t *testing.T) {
			sw := newFakeSwitch(t)
			sw.member(c.collection, c.key, c.attributes)
			p := configuredProvider(t, sw)
			state, errs := p.importState(c.typeName, c.key)
			expectOK(t, "import", errs)
			request := "GET " + c.collection + "/" + c.key
			for _, failure := range []map[string]bool{sw.refuse, sw.drop} {
				failure[request] = true
				_, errs = p.read(c.typeName, state)
				expectError(t, "read", errs, "could not read")
				delete(failure, request)
			}
		})
	}
}

func TestProviderFailsWhenTheSwitchRefusesTheLogin(t *testing.T) {
	sw := newFakeSwitch(t)
	p := newTestProvider(t)
	errs := p.configure(sw.hostname(), fakeUsername, "wrong")
	expectError(t, "configure", errs, "could not log in to the switch: 401 Unauthorized")
}

func TestProviderFailsWhenTheSwitchDoesNotAnswer(t *testing.T) {
	sw := newFakeSwitch(t)
	sw.drop["POST /login"] = true
	p := newTestProvider(t)
	errs := p.configure(sw.hostname(), fakeUsername, fakePassword)
	expectError(t, "configure", errs, "no response to POST")
}

// aoscx_l2_interface sets the port's VLANs and nothing else, so the
// description and user_config aoscx_interface and aoscx_port_settings manage
// stay as they are.
func TestL2InterfaceSetsOnlyVlans(t *testing.T) {
	if _, has := resourceL2Interface().Schema["description"]; has {
		t.Errorf("aoscx_l2_interface has a description")
	}
	if _, has := resourceL2Interface().Schema["admin_state"]; has {
		t.Errorf("aoscx_l2_interface has an admin_state")
	}
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)

	config := p.config("aoscx_l2_interface", map[string]interface{}{"interface": "1/1/1", "vlan_tag": 10})
	state, errs := p.create("aoscx_l2_interface", config)
	expectOK(t, "create", errs)
	expectRequests(t, sw.requests(),
		"GET /system/vlans/10",
		"PATCH "+port1+` {"vlan_mode": "access", "vlan_tag": {"10": "/rest/v10.16/system/vlans/10"}, "vlan_trunks": {}}`,
		"GET "+port1+"?selector=writable",
	)
	expectState(t, state, `{"id": "1/1/1", "interface": "1/1/1", "vlan_mode": "access", "vlan_tag": 10}`)
	p.expectNoChanges(t, "aoscx_l2_interface", state, config)

	config = p.config("aoscx_l2_interface", map[string]interface{}{
		"interface": "1/1/1", "vlan_mode": "trunk", "vlan_tag": 1, "vlan_ids": []int{1, 20},
	})
	state, errs = p.apply("aoscx_l2_interface", state, config)
	expectOK(t, "update", errs)
	expectRequests(t, sw.writes(), "PATCH "+port1+` {"vlan_mode": "native-untagged", "vlan_tag": {"1": "/rest/v10.16/system/vlans/1"},
		"vlan_trunks": {"1": "/rest/v10.16/system/vlans/1", "20": "/rest/v10.16/system/vlans/20"}}`)
	expectState(t, state, `{"vlan_mode": "trunk", "vlan_tag": 1, "vlan_ids": [1, 20], "trunk_allowed_all": false, "native_vlan_tag": false}`)
	p.expectNoChanges(t, "aoscx_l2_interface", state, config)
	expectAttributes(t, sw, port1, `{"description": "old", "admin": null, "admin_state": "up", "user_config": {"mtu": 9000},
		"vlan_mode": "native-untagged", "vlan_tag": {"1": "/rest/v10.16/system/vlans/1"},
		"vlan_trunks": {"1": "/rest/v10.16/system/vlans/1", "20": "/rest/v10.16/system/vlans/20"}}`)

	change, errs := p.plan("aoscx_l2_interface", state, p.config("aoscx_l2_interface", map[string]interface{}{
		"interface": "1/1/2", "vlan_mode": "trunk", "vlan_tag": 1, "vlan_ids": []int{1, 20},
	}))
	expectOK(t, "plan", errs)
	if !change.replace {
		t.Errorf("a new interface does not replace the resource")
	}
}

func TestL2InterfaceRefusesAVlanTheSwitchLacks(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)
	_, errs := p.create("aoscx_l2_interface", p.config("aoscx_l2_interface", map[string]interface{}{
		"interface": "1/1/1", "vlan_mode": "trunk", "vlan_tag": 1, "vlan_ids": []int{1, 30},
	}))
	expectError(t, "create", errs, "VLAN 30 could not be read from the switch")
	expectRequests(t, sw.writes())
}

// A port whose access VLAN reads back empty is on VLAN 1, and a failed read
// of the port is an error that keeps it.
func TestL2InterfaceReads(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)
	config := p.config("aoscx_l2_interface", map[string]interface{}{"interface": "1/1/1"})
	state, errs := p.create("aoscx_l2_interface", config)
	expectOK(t, "create", errs)
	sw.resource(port1, `{"vlan_mode": "access", "vlan_tag": null, "vlan_trunks": null}`)
	state, errs = p.read("aoscx_l2_interface", state)
	expectOK(t, "read", errs)
	expectState(t, state, `{"vlan_mode": "access", "vlan_tag": 1}`)
	p.expectNoChanges(t, "aoscx_l2_interface", state, config)

	sw.drop["GET "+port1] = true
	_, errs = p.read("aoscx_l2_interface", state)
	expectError(t, "read", errs, "Error in reading 1/1/1: no response to GET")
}

// Destroying a VLAN only forgets it: deleting it would take it off every
// port. A VLAN is forgotten when the switch answers 404 for it, and any other
// failed read is an error.
func TestVlanDestroyForgetsAndOnlyA404ForgetsIt(t *testing.T) {
	sw := newFakeSwitch(t)
	portOnSwitch(sw)
	p := configuredProvider(t, sw)
	config := p.config("aoscx_vlan", map[string]interface{}{"vlan_id": 10, "name": "ten"})
	state, errs := p.create("aoscx_vlan", config)
	expectOK(t, "create", errs)
	p.expectNoChanges(t, "aoscx_vlan", state, config)
	sw.requests()

	expectOK(t, "destroy", p.destroy("aoscx_vlan", state))
	expectRequests(t, sw.requests())
	expectAttributes(t, sw, "/system/vlans/10", `{"id": 10, "name": "ten", "admin": "up"}`)

	sw.drop["GET /system/vlans/10"] = true
	_, errs = p.read("aoscx_vlan", state)
	expectError(t, "read", errs, "Error in reading VLAN 10: no response to GET")
	_, errs = p.apply("aoscx_vlan", state, p.config("aoscx_vlan", map[string]interface{}{"vlan_id": 10, "name": "TEN"}))
	expectError(t, "update", errs, "Error in reading VLAN 10")
	expectRequests(t, sw.writes())

	delete(sw.drop, "GET /system/vlans/10")
	delete(sw.members["/system/vlans"], "10")
	read, errs := p.read("aoscx_vlan", state)
	expectOK(t, "read of a VLAN gone", errs)
	if !read.IsNull() {
		t.Errorf("a VLAN the switch answers 404 for is kept: %v", attributes(read))
	}
}
